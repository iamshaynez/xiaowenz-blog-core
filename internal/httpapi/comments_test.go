package httpapi

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/iamshaynez/xiaowenz-blog-core/internal/mailer"
	"github.com/iamshaynez/xiaowenz-blog-core/internal/turnstile"
)

func TestCommentSubmitNotifiesAdmin(t *testing.T) {
	srv, st, fm := newTestServer(t)
	rec := doJSON(t, srv, http.MethodPost, "/api/comments", map[string]string{
		"post": "ep-01", "name": "小明", "email": "ming@x.com",
		"content": "说得真好", "turnstile_token": "t",
	}, nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body=%s", rec.Code, rec.Body)
	}

	pending, err := st.ListComments("pending")
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending = %v, %v", pending, err)
	}
	c := pending[0]
	if c.Status != "pending" || c.IPHash == "" {
		t.Errorf("comment = %+v", c)
	}

	// Admin notification with signed one-click links.
	if len(fm.Sent) != 1 {
		t.Fatalf("admin notify emails = %d, want 1", len(fm.Sent))
	}
	m := fm.Sent[0]
	if !strings.Contains(m.HTML, "/api/comments/moderate?token=") {
		t.Error("moderate link missing from notify email")
	}
	if !strings.Contains(m.HTML, "approve") || !strings.Contains(m.HTML, "reject") {
		t.Error("approve/reject actions missing from notify email")
	}
}

// 邮箱是选填项：不留下邮箱的评论照样入库、照样通知管理员；
// 留了但格式非法仍然拒绝。
func TestCommentWithoutEmail(t *testing.T) {
	srv, st, fm := newTestServer(t)
	rec := doJSON(t, srv, http.MethodPost, "/api/comments", map[string]string{
		"post": "ep-01", "name": "小明", "content": "不留邮箱也想说话",
		"turnstile_token": "t",
	}, nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body=%s", rec.Code, rec.Body)
	}

	pending, err := st.ListComments("pending")
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending = %v, %v", pending, err)
	}
	if pending[0].AuthorEmail != "" {
		t.Errorf("AuthorEmail = %q, want empty", pending[0].AuthorEmail)
	}
	if len(fm.Sent) != 1 {
		t.Errorf("admin notify emails = %d, want 1", len(fm.Sent))
	}

	// 但留了格式非法的邮箱仍然 400（见 TestCommentValidation 的 bad email 用例）。
}

func TestCommentValidation(t *testing.T) {
	srv, _, _ := newTestServer(t)
	cases := []map[string]string{
		{"post": "", "name": "n", "email": "a@b.c", "content": "c"},                        // missing post
		{"post": "p", "name": "", "email": "a@b.c", "content": "c"},                        // empty name
		{"post": "p", "name": strings.Repeat("x", 51), "email": "a@b.c", "content": "c"},   // long name
		{"post": "p", "name": "n", "email": "bad", "content": "c"},                         // bad email
		{"post": "p", "name": "n", "email": "a@b.c", "content": ""},                        // empty content
		{"post": "p", "name": "n", "email": "a@b.c", "content": strings.Repeat("x", 2001)}, // long content
	}
	for i, body := range cases {
		body["turnstile_token"] = "t"
		rec := doJSON(t, srv, http.MethodPost, "/api/comments", body, nil)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("case %d: status = %d, want 400", i, rec.Code)
		}
	}
}

func TestCommentHoneypotAndRateLimit(t *testing.T) {
	srv, st, fm := newTestServer(t)

	// Honeypot: accepted, nothing stored, no notify.
	rec := doJSON(t, srv, http.MethodPost, "/api/comments", map[string]string{
		"post": "p", "name": "n", "email": "a@b.c", "content": "c",
		"turnstile_token": "t", "website": "spam",
	}, nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("honeypot: %d", rec.Code)
	}
	if n, _ := st.CountComments(""); n != 0 {
		t.Error("honeypot stored a comment")
	}
	if len(fm.Sent) != 0 {
		t.Error("honeypot notified admin")
	}

	// Rate limit: 10/hour.
	srv.comLimit = newTestLimiter(10)
	body := map[string]string{
		"post": "p", "name": "n", "email": "a@b.c", "content": "ok", "turnstile_token": "t",
	}
	for i := 0; i < 10; i++ {
		if rec := doJSON(t, srv, http.MethodPost, "/api/comments", body, nil); rec.Code != http.StatusAccepted {
			t.Fatalf("request %d: %d", i+1, rec.Code)
		}
	}
	if rec := doJSON(t, srv, http.MethodPost, "/api/comments", body, nil); rec.Code != http.StatusTooManyRequests {
		t.Errorf("11th comment: %d, want 429", rec.Code)
	}
}

func TestCommentTurnstileRejected(t *testing.T) {
	srv, _, _ := newTestServer(t)
	srv.verifier = &failingVerifier{}
	rec := doJSON(t, srv, http.MethodPost, "/api/comments", map[string]string{
		"post": "p", "name": "n", "email": "a@b.c", "content": "c", "turnstile_token": "bad",
	}, nil)
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}

func TestListApprovedCommentsPublic(t *testing.T) {
	srv, st, _ := newTestServer(t)
	id, _ := st.CreateComment("ep-01", "小明", "secret@x.com", "第一条", "h", "u")
	id2, _ := st.CreateComment("ep-01", "阿华", "s2@x.com", "第二条", "h", "u")
	_, _ = st.CreateComment("ep-01", "待审", "s3@x.com", "待审核", "h", "u")
	if err := st.ModerateComment(id, "approved"); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if err := st.ModerateComment(id2, "approved"); err != nil {
		t.Fatalf("approve2: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/comments?post=ep-01", nil)
	rec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var resp struct {
		Comments []map[string]any `json:"comments"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Comments) != 2 {
		t.Fatalf("approved = %d, want 2", len(resp.Comments))
	}
	if resp.Comments[0]["content"] != "第一条" || resp.Comments[1]["content"] != "第二条" {
		t.Errorf("order/projection wrong: %v", resp.Comments)
	}
	if _, leaked := resp.Comments[0]["author_email"]; leaked {
		t.Error("author_email must not be exposed")
	}
	// RFC3339 timestamps per SPEC §0.
	ts, _ := resp.Comments[0]["created_at"].(string)
	if !strings.Contains(ts, "T") || !strings.HasSuffix(ts, "Z") {
		t.Errorf("created_at = %q, want RFC3339 UTC", ts)
	}

	// Missing post param → 400.
	req = httptest.NewRequest(http.MethodGet, "/api/comments", nil)
	rec = httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("no post param: %d, want 400", rec.Code)
	}
}

func TestModerateOneClickLinks(t *testing.T) {
	srv, st, fm := newTestServer(t)
	rec := doJSON(t, srv, http.MethodPost, "/api/comments", map[string]string{
		"post": "ep-09", "name": "n", "email": "a@b.c", "content": "c", "turnstile_token": "t",
	}, nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("submit: %d", rec.Code)
	}
	pending, _ := st.ListComments("pending")
	if len(pending) != 1 {
		t.Fatalf("pending = %d", len(pending))
	}
	cid := pending[0].ID

	// Extract approve token from the admin email.
	body := fm.Sent[0].HTML
	idx := strings.Index(body, "/api/comments/moderate?token=")
	if idx < 0 {
		t.Fatal("no moderate link in notify email")
	}
	rest := body[idx+len("/api/comments/moderate?token="):]
	token := rest[:strings.IndexAny(rest, "\"'<")]
	if !strings.Contains(token, "approve") {
		t.Fatalf("first link should be approve, got %q", token)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/comments/moderate?token="+token, nil)
	rec = httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("moderate approve: %d; body=%s", rec.Code, rec.Body)
	}
	c, _ := st.GetComment(cid)
	if c.Status != "approved" {
		t.Errorf("status = %q, want approved", c.Status)
	}

	// Tampered token rejected.
	req = httptest.NewRequest(http.MethodGet, "/api/comments/moderate?token="+token+"xx", nil)
	rec = httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("tampered token: %d, want 403", rec.Code)
	}

	// Reject path via freshly signed token.
	rtok := srv.signModerate(cid, "reject")
	req = httptest.NewRequest(http.MethodGet, "/api/comments/moderate?token="+rtok, nil)
	rec = httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("moderate reject: %d", rec.Code)
	}
	c, _ = st.GetComment(cid)
	if c.Status != "rejected" {
		t.Errorf("status = %q, want rejected", c.Status)
	}
}

// With CORE_ADMIN_TOKEN empty the moderation signing key degrades to a
// publicly derivable constant, so forged approve/reject links would be
// accepted. Empty token must disable one-click moderation like it disables
// admin login (SPEC §1: 空 = admin 拒绝登录).
func TestModerateDisabledWhenAdminTokenEmpty(t *testing.T) {
	srv, st, _ := newTestServer(t)
	cid, err := st.CreateComment("ep-09", "n", "a@b.c", "c", "hash", "ua")
	if err != nil {
		t.Fatalf("seed comment: %v", err)
	}

	// A second server over the same store with an empty admin token: its
	// signing key is exactly the constant an outside attacker can compute.
	empty := *srv.cfg
	empty.AdminToken = ""
	forged := NewServer(Deps{
		Store: st, Mailer: &mailer.Fake{}, Verifier: &turnstile.Fake{Result: true},
		Config: &empty, Logger: slog.New(slog.DiscardHandler),
	})
	t.Cleanup(forged.Close)

	req := httptest.NewRequest(http.MethodGet, "/api/comments/moderate?token="+forged.signModerate(cid, "approve"), nil)
	rec := httptest.NewRecorder()
	forged.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("forged moderate link accepted: %d, want 403", rec.Code)
	}
	c, _ := st.GetComment(cid)
	if c.Status != "pending" {
		t.Errorf("status = %q, want still pending", c.Status)
	}
}

// A post slug of 200 runes is generous for any real permalink; an unbounded
// field lets junk rows land in the DB and bloat the moderation console.
func TestCommentPostLengthCapped(t *testing.T) {
	srv, st, _ := newTestServer(t)
	long := strings.Repeat("p", 201)
	rec := doJSON(t, srv, http.MethodPost, "/api/comments", map[string]string{
		"post": long, "name": "n", "email": "a@b.c", "content": "c", "turnstile_token": "t",
	}, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("201-rune post: %d, want 400", rec.Code)
	}
	pending, _ := st.ListComments("pending")
	if len(pending) != 0 {
		t.Errorf("pending = %d, want 0 (nothing stored)", len(pending))
	}

	// 200 runes still accepted.
	ok := strings.Repeat("p", 200)
	rec = doJSON(t, srv, http.MethodPost, "/api/comments", map[string]string{
		"post": ok, "name": "n", "email": "a@b.c", "content": "c", "turnstile_token": "t",
	}, nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("200-rune post: %d, want 202", rec.Code)
	}
}

// A moderation link is a capability carried inside an email; a leaked link
// must not stay valid forever. Tokens carry an expiry covered by the HMAC.
func TestModerateTokenExpires(t *testing.T) {
	srv, st, _ := newTestServer(t)
	cid, err := st.CreateComment("ep-10", "n", "a@b.c", "c", "hash", "ua")
	if err != nil {
		t.Fatalf("seed comment: %v", err)
	}

	expired := srv.signModerateAt(cid, "approve", time.Now().Add(-time.Minute))
	req := httptest.NewRequest(http.MethodGet, "/api/comments/moderate?token="+expired, nil)
	rec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("expired moderate token: %d, want 403", rec.Code)
	}
	c, _ := st.GetComment(cid)
	if c.Status != "pending" {
		t.Errorf("status = %q, want still pending", c.Status)
	}

	fresh := srv.signModerateAt(cid, "approve", time.Now().Add(time.Minute))
	req = httptest.NewRequest(http.MethodGet, "/api/comments/moderate?token="+fresh, nil)
	rec = httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("fresh moderate token: %d, want 200", rec.Code)
	}
}
