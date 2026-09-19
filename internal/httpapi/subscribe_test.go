package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func jsonBody(t *testing.T, v any) *bytes.Reader {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return bytes.NewReader(b)
}

func TestSubscribeNewSendsVerifyEmail(t *testing.T) {
	srv, st, fm := newTestServer(t)
	rec := doJSON(t, srv, http.MethodPost, "/api/subscribe",
		map[string]string{"email": "New@Example.com", "turnstile_token": "tok"}, nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body=%s", rec.Code, rec.Body)
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response not JSON: %v", err)
	}
	if resp["ok"] != true {
		t.Errorf("resp = %v", resp)
	}

	sub, err := st.GetSubscriberByEmail("new@example.com")
	if err != nil {
		t.Fatalf("subscriber not stored: %v", err)
	}
	if sub.Status != "pending" {
		t.Errorf("status = %q, want pending", sub.Status)
	}

	if len(fm.Sent) != 1 {
		t.Fatalf("emails = %d, want 1", len(fm.Sent))
	}
	m := fm.Sent[0]
	if m.To != "new@example.com" {
		t.Errorf("to = %q", m.To)
	}
	if !strings.Contains(m.HTML, "https://core.test/api/verify?token="+sub.ConfirmToken) {
		t.Error("verify link with token missing from email HTML")
	}
	if !strings.Contains(m.Text, sub.ConfirmToken) {
		t.Error("verify token missing from text alternative")
	}
}

func TestSubscribeHoneypotSilentlyAccepted(t *testing.T) {
	srv, st, fm := newTestServer(t)
	rec := doJSON(t, srv, http.MethodPost, "/api/subscribe",
		map[string]string{"email": "bot@x.com", "website": "http://spam", "turnstile_token": "t"}, nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("honeypot must still get 202, got %d", rec.Code)
	}
	if len(fm.Sent) != 0 {
		t.Error("honeypot triggered an email")
	}
	if _, err := st.GetSubscriberByEmail("bot@x.com"); err == nil {
		t.Error("honeypot created a subscriber row")
	}
}

func TestSubscribeTurnstileFailureRejected(t *testing.T) {
	srv, _, fm := newTestServer(t)
	srv.verifier = &failingVerifier{}
	rec := doJSON(t, srv, http.MethodPost, "/api/subscribe",
		map[string]string{"email": "a@x.com", "turnstile_token": "bad"}, nil)
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
	var resp map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp["error"] == "" {
		t.Error("error body must be {\"error\":...}")
	}
	if len(fm.Sent) != 0 {
		t.Error("no email must be sent on turnstile failure")
	}
}

type failingVerifier struct{}

func (failingVerifier) Verify(_ context.Context, _, _ string) (bool, error) { return false, nil }

func TestSubscribeInvalidEmail(t *testing.T) {
	srv, _, _ := newTestServer(t)
	for _, email := range []string{"", "not-an-email", "a@", "@b.com"} {
		rec := doJSON(t, srv, http.MethodPost, "/api/subscribe",
			map[string]string{"email": email, "turnstile_token": "t"}, nil)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("email %q: status = %d, want 400", email, rec.Code)
		}
	}
}

func TestSubscribeRateLimited(t *testing.T) {
	srv, _, _ := newTestServer(t)
	srv.subLimit = newTestLimiter(5)
	body := map[string]string{"email": "rl@x.com", "turnstile_token": "t"}
	for i := 0; i < 5; i++ {
		rec := doJSON(t, srv, http.MethodPost, "/api/subscribe", body, nil)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("request %d: status = %d, want 202", i+1, rec.Code)
		}
	}
	rec := doJSON(t, srv, http.MethodPost, "/api/subscribe", body, nil)
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("6th request: status = %d, want 429", rec.Code)
	}
}

func TestSubscribeIdempotentBranches(t *testing.T) {
	srv, st, fm := newTestServer(t)

	// Branch 1: already active → 202, no email.
	sub, err := st.UpsertPending("active@x.com", "tok", time.Now().Add(24*time.Hour))
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := st.Activate(sub.ID); err != nil {
		t.Fatalf("seed activate: %v", err)
	}
	rec := doJSON(t, srv, http.MethodPost, "/api/subscribe",
		map[string]string{"email": "active@x.com", "turnstile_token": "t"}, nil)
	if rec.Code != http.StatusAccepted {
		t.Errorf("active resubscribe: %d", rec.Code)
	}
	if len(fm.Sent) != 0 {
		t.Error("active resubscribe must not send email")
	}

	// Branch 2: pending → resend verify email with refreshed token.
	old, _ := st.UpsertPending("pend@x.com", "oldtok", time.Now().Add(time.Hour))
	rec = doJSON(t, srv, http.MethodPost, "/api/subscribe",
		map[string]string{"email": "pend@x.com", "turnstile_token": "t"}, nil)
	if rec.Code != http.StatusAccepted {
		t.Errorf("pending resubscribe: %d", rec.Code)
	}
	if len(fm.Sent) != 1 {
		t.Fatalf("pending resubscribe must resend, emails=%d", len(fm.Sent))
	}
	now, _ := st.GetSubscriberByEmail("pend@x.com")
	if now.ConfirmToken == old.ConfirmToken {
		t.Error("pending resubscribe must refresh token")
	}
	if !strings.Contains(fm.Sent[0].HTML, now.ConfirmToken) {
		t.Error("resent email must carry refreshed token")
	}

	// Branch 3: unsubscribed → back through verification.
	us, _ := st.UpsertPending("gone@x.com", "tok", time.Now().Add(time.Hour))
	_ = st.Activate(us.ID)
	if err := st.UnsubscribeByToken(us.UnsubToken); err != nil {
		t.Fatalf("seed unsub: %v", err)
	}
	rec = doJSON(t, srv, http.MethodPost, "/api/subscribe",
		map[string]string{"email": "gone@x.com", "turnstile_token": "t"}, nil)
	if rec.Code != http.StatusAccepted {
		t.Errorf("unsubscribed resubscribe: %d", rec.Code)
	}
	back, _ := st.GetSubscriberByEmail("gone@x.com")
	if back.Status != "pending" {
		t.Errorf("status = %q, want pending (re-verify)", back.Status)
	}
}

func TestVerifyActivatesAndRedirects(t *testing.T) {
	srv, st, fm := newTestServer(t)
	sub, _ := st.UpsertPending("v@x.com", "validtok", time.Now().Add(24*time.Hour))

	req := httptest.NewRequest(http.MethodGet, "/api/verify?token=validtok", nil)
	rec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "https://site.test/subscribed/" {
		t.Errorf("Location = %q", loc)
	}
	after, _ := st.GetSubscriberByEmail("v@x.com")
	if after.Status != "active" {
		t.Errorf("status = %q, want active", after.Status)
	}
	if len(fm.Sent) != 1 || !strings.Contains(fm.Sent[0].HTML, "unsub?token=") {
		t.Error("welcome email with unsub link must be sent")
	}
	_ = sub
}

func TestVerifyBadOrExpiredRedirects(t *testing.T) {
	srv, st, _ := newTestServer(t)

	// Unknown token.
	req := httptest.NewRequest(http.MethodGet, "/api/verify?token=nope", nil)
	rec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "https://site.test/subscribe-expired/" {
		t.Errorf("bad token: code=%d loc=%q", rec.Code, rec.Header().Get("Location"))
	}

	// Expired token.
	if _, err := st.UpsertPending("old@x.com", "expiredtok", time.Now().Add(-time.Hour)); err != nil {
		t.Fatalf("seed: %v", err)
	}
	req = httptest.NewRequest(http.MethodGet, "/api/verify?token=expiredtok", nil)
	rec = httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, req)
	if loc := rec.Header().Get("Location"); rec.Code != http.StatusFound || loc != "https://site.test/subscribe-expired/" {
		t.Errorf("expired token: code=%d loc=%q", rec.Code, loc)
	}
}

func TestUnsubFlow(t *testing.T) {
	srv, st, _ := newTestServer(t)
	sub, _ := st.UpsertPending("u@x.com", "tok", time.Now().Add(time.Hour))
	_ = st.Activate(sub.ID)

	// GET shows a confirmation page embedding the token (anti-prefetch).
	req := httptest.NewRequest(http.MethodGet, "/api/unsub?token="+sub.UnsubToken, nil)
	rec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET unsub: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), sub.UnsubToken) {
		t.Error("confirm page must embed the token in a form")
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Errorf("content-type = %q", ct)
	}
	// Still active: GET must not unsubscribe (prefetch-safe).
	still, _ := st.GetSubscriberByEmail("u@x.com")
	if still.Status != "active" {
		t.Error("GET /api/unsub must not unsubscribe")
	}

	// POST executes.
	req = httptest.NewRequest(http.MethodPost, "/api/unsub",
		strings.NewReader("token="+sub.UnsubToken))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST unsub: %d; body=%s", rec.Code, rec.Body)
	}
	gone, _ := st.GetSubscriberByEmail("u@x.com")
	if gone.Status != "unsubscribed" {
		t.Errorf("status = %q, want unsubscribed", gone.Status)
	}
}

func TestUnsubBadToken(t *testing.T) {
	srv, _, _ := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/unsub?token=missing", nil)
	rec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("GET bad token: %d, want 400", rec.Code)
	}
	req = httptest.NewRequest(http.MethodPost, "/api/unsub", strings.NewReader("token=missing"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, req)
	if rec.Code == http.StatusOK {
		t.Error("POST bad token must not succeed")
	}
}

func TestCORSPolicy(t *testing.T) {
	srv, _, _ := newTestServer(t)
	rec := doJSON(t, srv, http.MethodPost, "/api/subscribe",
		map[string]string{"email": "cors@x.com", "turnstile_token": "t"}, nil)
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://site.test" {
		t.Errorf("ACAO = %q, want https://site.test", got)
	}

	// Preflight.
	req := httptest.NewRequest(http.MethodOptions, "/api/subscribe", nil)
	rec = httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Errorf("OPTIONS: %d, want 204", rec.Code)
	}
	if rec.Header().Get("Access-Control-Allow-Methods") == "" {
		t.Error("preflight must allow methods")
	}
}
