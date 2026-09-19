package httpapi

import (
	"bytes"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamshaynez/xiaowenz-blog-core/internal/config"
	"github.com/iamshaynez/xiaowenz-blog-core/internal/db"
	"github.com/iamshaynez/xiaowenz-blog-core/internal/mailer"
	"github.com/iamshaynez/xiaowenz-blog-core/internal/store"
	"github.com/iamshaynez/xiaowenz-blog-core/internal/turnstile"
)

// When no admin notify address is derivable, the comment must still be
// accepted but the skip must be visible in logs — not silent.
func TestCommentNotifySkippedLogsWarn(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	var logBuf bytes.Buffer
	cfg := &config.Config{
		BaseURL:      "https://core.test",
		SiteURL:      "https://site.test",
		AdminToken:   "tok",
		SMTPFrom:     "", // no From → no derivable admin recipient
		SendInterval: time.Millisecond,
	}
	srv := NewServer(Deps{
		Store:    store.New(d),
		Mailer:   &mailer.Fake{},
		Verifier: &turnstile.Fake{Result: true},
		Config:   cfg,
		Logger:   slog.New(slog.NewTextHandler(&logBuf, nil)),
	})
	t.Cleanup(srv.Close)

	rec := doJSON(t, srv, http.MethodPost, "/api/comments", map[string]string{
		"post": "ep-01", "name": "n", "email": "a@b.c", "content": "c", "turnstile_token": "t",
	}, nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("comment must still be accepted: %d", rec.Code)
	}
	out := logBuf.String()
	if !strings.Contains(out, "WARN") || !strings.Contains(out, "notify") {
		t.Errorf("expected WARN about skipped admin notify, got: %s", out)
	}
}
