package httpapi

import (
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/xiaowenz/xiaowen-core/internal/config"
	"github.com/xiaowenz/xiaowen-core/internal/db"
	"github.com/xiaowenz/xiaowen-core/internal/mailer"
	"github.com/xiaowenz/xiaowen-core/internal/ratelimit"
	"github.com/xiaowenz/xiaowen-core/internal/store"
	"github.com/xiaowenz/xiaowen-core/internal/turnstile"
)

// newTestServer wires a Server against a real temp SQLite db, a fake mailer
// and a fake turnstile verifier — the full handler stack, zero externals.
func newTestServer(t *testing.T) (*Server, *store.Store, *mailer.Fake) {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	st := store.New(d)
	fm := &mailer.Fake{}
	cfg := &config.Config{
		Addr:           ":0",
		BaseURL:        "https://core.test",
		SiteURL:        "https://site.test",
		AdminToken:     "test-admin-token",
		SMTPFrom:       "三杯过后 <letter@test.dev>",
		MailingAddress: "Test Road 1",
		SendInterval:   time.Millisecond,
	}
	srv := NewServer(Deps{
		Store:    st,
		Mailer:   fm,
		Verifier: &turnstile.Fake{Result: true},
		Config:   cfg,
		Logger:   slog.New(slog.DiscardHandler),
	})
	t.Cleanup(srv.Close)
	return srv, st, fm
}

func doJSON(t *testing.T, srv *Server, method, path string, body any, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, jsonBody(t, body))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, req)
	return rec
}

// newTestLimiter builds a limiter with the production window but a small
// burst so rate-limit tests stay fast and deterministic.
func newTestLimiter(n int) *ratelimit.Limiter {
	return ratelimit.New(n, time.Hour)
}
