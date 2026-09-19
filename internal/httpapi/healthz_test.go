package httpapi

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/xiaowenz/xiaowen-core/internal/config"
	"github.com/xiaowenz/xiaowen-core/internal/db"
	"github.com/xiaowenz/xiaowen-core/internal/mailer"
	"github.com/xiaowenz/xiaowen-core/internal/store"
	"github.com/xiaowenz/xiaowen-core/internal/turnstile"
)

func TestHealthz503WhenDBDown(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	srv := NewServer(Deps{
		Store:    store.New(d),
		Mailer:   &mailer.Fake{},
		Verifier: &turnstile.Fake{Result: true},
		Config:   &config.Config{BaseURL: "https://core.test", SiteURL: "https://site.test", SendInterval: time.Millisecond},
		Logger:   slog.New(slog.DiscardHandler),
	})
	t.Cleanup(srv.Close)

	// Kill the db out from under the server.
	if err := d.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	var resp map[string]bool
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["ok"] || resp["db"] {
		t.Errorf("healthz = %v, want {ok:false, db:false}", resp)
	}
}
