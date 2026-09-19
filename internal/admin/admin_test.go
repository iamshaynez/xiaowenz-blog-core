package admin

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xiaowenz/xiaowen-core/internal/config"
	"github.com/xiaowenz/xiaowen-core/internal/db"
	"github.com/xiaowenz/xiaowen-core/internal/mailer"
	"github.com/xiaowenz/xiaowen-core/internal/newsletter"
	"github.com/xiaowenz/xiaowen-core/internal/s3store"
	"github.com/xiaowenz/xiaowen-core/internal/store"
)

type adminFixture struct {
	handler http.Handler
	store   *store.Store
	s3      *s3store.Fake
	mailer  *mailer.Fake
	sender  *newsletter.Sender
	cfg     *config.Config
}

func newAdminFixture(t *testing.T) *adminFixture {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	st := store.New(d)
	cfg := &config.Config{
		BaseURL:        "https://core.test",
		SiteURL:        "https://site.test",
		AdminToken:     "test-admin-token",
		SMTPFrom:       "三杯过后 <letter@test.dev>",
		MailingAddress: "Test Road 1",
		SendInterval:   time.Millisecond,
	}
	s3 := s3store.NewFake("https://media.test")
	fm := &mailer.Fake{}
	sender := newsletter.NewSender(st, fm, cfg, slog.New(slog.DiscardHandler))
	h := NewHandler(Deps{
		Store:  st,
		Config: cfg,
		S3:     s3,
		Sender: sender,
		Logger: slog.New(slog.DiscardHandler),
	})
	return &adminFixture{handler: h.Routes(), store: st, s3: s3, mailer: fm, sender: sender, cfg: cfg}
}

// login performs the admin login and returns the session cookie.
func login(t *testing.T, h http.Handler) *http.Cookie {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/admin/login",
		strings.NewReader("token=test-admin-token"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("login status = %d, want 303", rec.Code)
	}
	var session *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookieName {
			session = c
		}
	}
	if session == nil {
		t.Fatal("no session cookie set")
	}
	if !session.HttpOnly {
		t.Error("session cookie must be HttpOnly")
	}
	if session.SameSite != http.SameSiteLaxMode {
		t.Error("session cookie must be SameSite=Lax")
	}
	return session
}

func authed(method, path string, body io.Reader, session *http.Cookie) *http.Request {
	req := httptest.NewRequest(method, path, body)
	req.AddCookie(session)
	return req
}

func TestLoginWrongTokenRejected(t *testing.T) {
	fx := newAdminFixture(t)
	req := httptest.NewRequest(http.MethodPost, "/admin/login", strings.NewReader("token=wrong"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	fx.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized && rec.Code != http.StatusForbidden {
		t.Errorf("wrong token status = %d, want 401/403", rec.Code)
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookieName && c.Value != "" {
			t.Error("session cookie must not be set on failed login")
		}
	}
}

func TestLoginPageServed(t *testing.T) {
	fx := newAdminFixture(t)
	req := httptest.NewRequest(http.MethodGet, "/admin/login", nil)
	rec := httptest.NewRecorder()
	fx.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "<form") {
		t.Errorf("login page: %d", rec.Code)
	}
}

func TestAuthMiddlewareGuardsAdmin(t *testing.T) {
	fx := newAdminFixture(t)

	// No cookie → redirect to login.
	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	rec := httptest.NewRecorder()
	fx.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/login" {
		t.Errorf("unauth /admin: %d loc=%q", rec.Code, rec.Header().Get("Location"))
	}

	// Forged cookie → redirect.
	req = httptest.NewRequest(http.MethodGet, "/admin", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "9999999999|deadbeef"})
	rec = httptest.NewRecorder()
	fx.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Errorf("forged cookie: %d", rec.Code)
	}

	// Valid cookie → 200.
	session := login(t, fx.handler)
	rec = httptest.NewRecorder()
	fx.handler.ServeHTTP(rec, authed(http.MethodGet, "/admin", nil, session))
	if rec.Code != http.StatusOK {
		t.Errorf("authed /admin: %d", rec.Code)
	}
}

func TestExpiredSessionRejected(t *testing.T) {
	fx := newAdminFixture(t)
	// Craft a validly-signed but expired cookie using the handler's own signer.
	fx.cfg.AdminToken = "test-admin-token"
	expired := signSession(fx.cfg.AdminToken, time.Now().Add(-time.Hour).Unix())
	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: expired})
	rec := httptest.NewRecorder()
	fx.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Errorf("expired session: %d, want 303", rec.Code)
	}
}

func TestAdminPOSTOriginChecked(t *testing.T) {
	fx := newAdminFixture(t)
	session := login(t, fx.handler)

	req := authed(http.MethodPost, "/admin/comments/1/approve", nil, session)
	req.Header.Set("Origin", "https://evil.example.com")
	rec := httptest.NewRecorder()
	fx.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("cross-origin POST: %d, want 403", rec.Code)
	}

	// Same-origin Origin passes the check (comment missing → 400/404, not 403).
	req = authed(http.MethodPost, "/admin/comments/1/approve", nil, session)
	req.Header.Set("Origin", "http://example.com") // httptest default Host
	rec = httptest.NewRecorder()
	fx.handler.ServeHTTP(rec, req)
	if rec.Code == http.StatusForbidden {
		t.Error("same-origin POST wrongly rejected")
	}
}

func TestDashboardRenders(t *testing.T) {
	fx := newAdminFixture(t)
	session := login(t, fx.handler)
	rec := httptest.NewRecorder()
	fx.handler.ServeHTTP(rec, authed(http.MethodGet, "/admin", nil, session))
	if rec.Code != http.StatusOK {
		t.Fatalf("dashboard: %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"订阅", "评论", "PV", "下载"} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard missing %q", want)
		}
	}
}

func TestSubscribersListAndFilter(t *testing.T) {
	fx := newAdminFixture(t)
	session := login(t, fx.handler)
	sub, _ := fx.store.UpsertPending("lisi@x.com", "t", time.Now().Add(time.Hour))
	_ = fx.store.Activate(sub.ID)
	_, _ = fx.store.UpsertPending("wang@y.com", "t", time.Now().Add(time.Hour))

	rec := httptest.NewRecorder()
	fx.handler.ServeHTTP(rec, authed(http.MethodGet, "/admin/subscribers", nil, session))
	if rec.Code != http.StatusOK {
		t.Fatalf("subscribers: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "lisi@x.com") || !strings.Contains(rec.Body.String(), "wang@y.com") {
		t.Error("subscriber rows missing")
	}

	rec = httptest.NewRecorder()
	fx.handler.ServeHTTP(rec, authed(http.MethodGet, "/admin/subscribers?status=active", nil, session))
	if !strings.Contains(rec.Body.String(), "lisi@x.com") || strings.Contains(rec.Body.String(), "wang@y.com") {
		t.Error("status filter broken")
	}

	rec = httptest.NewRecorder()
	fx.handler.ServeHTTP(rec, authed(http.MethodGet, "/admin/subscribers?q=y.com", nil, session))
	if strings.Contains(rec.Body.String(), "lisi@x.com") || !strings.Contains(rec.Body.String(), "wang@y.com") {
		t.Error("q filter broken")
	}
}

func TestSubscribersImportExportDelete(t *testing.T) {
	fx := newAdminFixture(t)
	session := login(t, fx.handler)
	// Pre-existing subscriber must be skipped on import.
	sub, _ := fx.store.UpsertPending("dup@x.com", "t", time.Now().Add(time.Hour))
	_ = fx.store.Activate(sub.ID)

	csv := "email,confirmed\ndup@x.com,1\nnew1@x.com,1\nnew2@x.com,\n"
	body, ct := makeMultipart(t, "file", "subs.csv", "text/csv", []byte(csv))
	req := authed(http.MethodPost, "/admin/subscribers/import", body, session)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	fx.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther && rec.Code != http.StatusOK {
		t.Fatalf("import: %d; body=%s", rec.Code, rec.Body)
	}
	got, err := fx.store.GetSubscriberByEmail("new1@x.com")
	if err != nil || got.Status != "active" {
		t.Errorf("imported subscriber: %v %+v", err, got)
	}
	if got.ConfirmedAt == 0 {
		t.Error("import must set confirmed_at")
	}
	n, _ := fx.store.CountSubscribers("", "")
	if n != 3 {
		t.Errorf("total = %d, want 3 (dupe skipped)", n)
	}

	// Export.
	rec = httptest.NewRecorder()
	fx.handler.ServeHTTP(rec, authed(http.MethodGet, "/admin/subscribers/export", nil, session))
	if rec.Code != http.StatusOK {
		t.Fatalf("export: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "new1@x.com") {
		t.Error("export missing imported email")
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/csv") {
		t.Errorf("export content-type = %q", ct)
	}

	// Delete.
	rec = httptest.NewRecorder()
	fx.handler.ServeHTTP(rec, authed(http.MethodPost, "/admin/subscribers/"+itoa(got.ID)+"/delete", nil, session))
	if rec.Code != http.StatusSeeOther && rec.Code != http.StatusOK {
		t.Fatalf("delete: %d", rec.Code)
	}
	if _, err := fx.store.GetSubscriberByEmail("new1@x.com"); err == nil {
		t.Error("deleted subscriber still present")
	}
}

func TestAdminCommentModeration(t *testing.T) {
	fx := newAdminFixture(t)
	session := login(t, fx.handler)
	id, _ := fx.store.CreateComment("ep-01", "n", "e@x.com", "内容", "h", "u")

	// Pending list shows it.
	rec := httptest.NewRecorder()
	fx.handler.ServeHTTP(rec, authed(http.MethodGet, "/admin/comments?status=pending", nil, session))
	if !strings.Contains(rec.Body.String(), "内容") {
		t.Error("pending list missing comment")
	}

	// Approve.
	rec = httptest.NewRecorder()
	fx.handler.ServeHTTP(rec, authed(http.MethodPost, "/admin/comments/"+itoa(id)+"/approve", nil, session))
	if rec.Code != http.StatusSeeOther && rec.Code != http.StatusOK {
		t.Fatalf("approve: %d", rec.Code)
	}
	c, _ := fx.store.GetComment(id)
	if c.Status != "approved" {
		t.Errorf("status = %q", c.Status)
	}

	// Reject.
	rec = httptest.NewRecorder()
	fx.handler.ServeHTTP(rec, authed(http.MethodPost, "/admin/comments/"+itoa(id)+"/reject", nil, session))
	c, _ = fx.store.GetComment(id)
	if c.Status != "rejected" {
		t.Errorf("status = %q", c.Status)
	}

	// Missing comment → 400/404.
	rec = httptest.NewRecorder()
	fx.handler.ServeHTTP(rec, authed(http.MethodPost, "/admin/comments/9999/approve", nil, session))
	if rec.Code == http.StatusOK || rec.Code == http.StatusSeeOther {
		t.Error("moderating missing comment must fail")
	}
}

func TestNewsletterCreatePreviewSend(t *testing.T) {
	fx := newAdminFixture(t)
	session := login(t, fx.handler)
	sub, _ := fx.store.UpsertPending("r@x.com", "t", time.Now().Add(time.Hour))
	_ = fx.store.Activate(sub.ID)

	// Create draft (goldmark renders markdown → html).
	form := url.Values{"subject": {"第 1 期"}, "markdown": {"# 你好\n\n这是**正文**。"}}
	createReq := authed(http.MethodPost, "/admin/newsletters", strings.NewReader(form.Encode()), session)
	createReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	fx.handler.ServeHTTP(rec, createReq)
	if rec.Code != http.StatusSeeOther && rec.Code != http.StatusOK {
		t.Fatalf("create newsletter: %d; body=%s", rec.Code, rec.Body)
	}
	list, _ := fx.store.ListNewsletters()
	if len(list) != 1 {
		t.Fatalf("newsletters = %d", len(list))
	}
	n := list[0]
	if n.Status != "draft" || !strings.Contains(n.HTML, "<strong>正文</strong>") {
		t.Errorf("draft = %+v", n)
	}

	// New form page renders.
	rec = httptest.NewRecorder()
	fx.handler.ServeHTTP(rec, authed(http.MethodGet, "/admin/newsletters/new", nil, session))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "markdown") {
		t.Errorf("new page: %d", rec.Code)
	}

	// Preview renders stored HTML.
	rec = httptest.NewRecorder()
	fx.handler.ServeHTTP(rec, authed(http.MethodGet, "/admin/newsletters/"+itoa(n.ID), nil, session))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "<strong>正文</strong>") {
		t.Errorf("preview: %d", rec.Code)
	}

	// Send → background sender delivers.
	rec = httptest.NewRecorder()
	fx.handler.ServeHTTP(rec, authed(http.MethodPost, "/admin/newsletters/"+itoa(n.ID)+"/send", nil, session))
	if rec.Code != http.StatusSeeOther && rec.Code != http.StatusOK {
		t.Fatalf("send: %d", rec.Code)
	}
	fx.sender.Wait()
	after, _ := fx.store.GetNewsletter(n.ID)
	if after.Status != "sent" || after.SentCount != 1 {
		t.Errorf("after send: %+v", after)
	}
	if len(fx.mailer.Sent) != 1 || !strings.Contains(fx.mailer.Sent[0].HTML, "unsub?token=") {
		t.Error("subscriber did not receive newsletter with unsub link")
	}

	// List page shows status.
	rec = httptest.NewRecorder()
	fx.handler.ServeHTTP(rec, authed(http.MethodGet, "/admin/newsletters", nil, session))
	if !strings.Contains(rec.Body.String(), "sent") {
		t.Error("list page missing status")
	}
}

func TestMediaUpload(t *testing.T) {
	fx := newAdminFixture(t)
	session := login(t, fx.handler)

	// Image upload → images/ prefix, media row, JSON response.
	req := func() *http.Request {
		b, ct := makeMultipart(t, "file", "cover.png", "image/png", []byte("pngbytes"))
		req := authed(http.MethodPost, "/admin/media", b, session)
		req.Header.Set("Content-Type", ct)
		return req
	}()
	rec := httptest.NewRecorder()
	fx.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("upload: %d; body=%s", rec.Code, rec.Body)
	}
	var resp struct {
		URL   string `json:"url"`
		Bytes int64  `json:"bytes"`
		Key   string `json:"key"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.HasPrefix(resp.Key, "images/") || resp.Bytes != 8 {
		t.Errorf("resp = %+v", resp)
	}
	if resp.URL != "https://media.test/"+resp.Key {
		t.Errorf("url = %q", resp.URL)
	}
	if _, err := fx.store.GetMediaByKey(resp.Key); err != nil {
		t.Errorf("media row missing: %v", err)
	}

	// Audio upload → audio/ prefix (drives /a/{slug}.mp3).
	req = func() *http.Request {
		b, ct := makeMultipart(t, "file", "ep9.mp3", "audio/mpeg", []byte("mp3"))
		req := authed(http.MethodPost, "/admin/media", b, session)
		req.Header.Set("Content-Type", ct)
		return req
	}()
	rec = httptest.NewRecorder()
	fx.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("audio upload: %d", rec.Code)
	}
	var resp2 struct{ Key string }
	_ = json.Unmarshal(rec.Body.Bytes(), &resp2)
	if resp2.Key != "audio/ep9.mp3" {
		t.Errorf("audio key = %q, want audio/ep9.mp3", resp2.Key)
	}

	// Reject disallowed mime.
	req = func() *http.Request {
		b, ct := makeMultipart(t, "file", "evil.exe", "application/x-msdownload", []byte("x"))
		req := authed(http.MethodPost, "/admin/media", b, session)
		req.Header.Set("Content-Type", ct)
		return req
	}()
	rec = httptest.NewRecorder()
	fx.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("exe upload: %d, want 400", rec.Code)
	}

	// Media list page.
	rec = httptest.NewRecorder()
	fx.handler.ServeHTTP(rec, authed(http.MethodGet, "/admin/media", nil, session))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "ep9.mp3") {
		t.Errorf("media list: %d", rec.Code)
	}
}

func TestMediaUploadConflictGetsSuffix(t *testing.T) {
	fx := newAdminFixture(t)
	session := login(t, fx.handler)
	for i := 0; i < 2; i++ {
		b, ct := makeMultipart(t, "file", "ep1.mp3", "audio/mpeg", []byte("v"+string(rune('1'+i))))
		req := authed(http.MethodPost, "/admin/media", b, session)
		req.Header.Set("Content-Type", ct)
		rec := httptest.NewRecorder()
		fx.handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("upload %d: %d", i, rec.Code)
		}
	}
	media, _ := fx.store.ListMedia()
	if len(media) != 2 {
		t.Fatalf("media rows = %d, want 2", len(media))
	}
	if media[0].Key == media[1].Key {
		t.Errorf("conflicting keys not disambiguated: %q", media[0].Key)
	}
	if !strings.HasPrefix(media[1].Key, "audio/ep1") {
		t.Errorf("second key = %q, want audio/ep1…", media[1].Key)
	}
}

func TestMediaUploadWithoutR2Degrades(t *testing.T) {
	fx := newAdminFixture(t)
	fx2handler := NewHandler(Deps{
		Store:  fx.store,
		Config: fx.cfg,
		S3:     nil, // R2 unconfigured
		Sender: fx.sender,
		Logger: slog.New(slog.DiscardHandler),
	}).Routes()
	session := login(t, fx2handler)
	req := func() *http.Request {
		b, ct := makeMultipart(t, "file", "a.png", "image/png", []byte("x"))
		req := authed(http.MethodPost, "/admin/media", b, session)
		req.Header.Set("Content-Type", ct)
		return req
	}()
	rec := httptest.NewRecorder()
	fx2handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("upload without R2: %d, want 503 graceful degradation", rec.Code)
	}
	var resp map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp["error"] == "" {
		t.Error("error body expected")
	}
}

func TestAdminStats(t *testing.T) {
	fx := newAdminFixture(t)
	session := login(t, fx.handler)
	_ = fx.store.RecordPageview("/a", "", "h1", "")
	_, _ = fx.store.RecordDownloadDedup("ep1", "h1", "", "")

	rec := httptest.NewRecorder()
	fx.handler.ServeHTTP(rec, authed(http.MethodGet, "/admin/stats", nil, session))
	if rec.Code != http.StatusOK {
		t.Fatalf("stats json: %d", rec.Code)
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["pv_30d"].(float64) != 1 {
		t.Errorf("stats = %v", resp)
	}
	dl, _ := resp["downloads_30d"].(map[string]any)
	if dl["ep1"].(float64) != 1 {
		t.Errorf("downloads = %v", dl)
	}

	rec = httptest.NewRecorder()
	fx.handler.ServeHTTP(rec, authed(http.MethodGet, "/admin/stats/html", nil, session))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "ep1") {
		t.Errorf("stats html: %d", rec.Code)
	}
}

func TestEmptyAdminTokenDeniesLogin(t *testing.T) {
	fx := newAdminFixture(t)
	fx.cfg.AdminToken = ""
	h := NewHandler(Deps{Store: fx.store, Config: fx.cfg, S3: fx.s3, Sender: fx.sender,
		Logger: slog.New(slog.DiscardHandler)}).Routes()
	req := httptest.NewRequest(http.MethodPost, "/admin/login", strings.NewReader("token="))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code == http.StatusSeeOther {
		t.Error("empty admin token must refuse login")
	}
}
