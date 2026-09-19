// Command core wires and runs the xiaowen-core service.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/xiaowenz/xiaowen-core/internal/admin"
	"github.com/xiaowenz/xiaowen-core/internal/backup"
	"github.com/xiaowenz/xiaowen-core/internal/config"
	"github.com/xiaowenz/xiaowen-core/internal/db"
	"github.com/xiaowenz/xiaowen-core/internal/httpapi"
	"github.com/xiaowenz/xiaowen-core/internal/mailer"
	"github.com/xiaowenz/xiaowen-core/internal/newsletter"
	"github.com/xiaowenz/xiaowen-core/internal/s3store"
	"github.com/xiaowenz/xiaowen-core/internal/store"
	"github.com/xiaowenz/xiaowen-core/internal/turnstile"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(log)

	cfg, err := config.Load()
	if err != nil {
		log.Error("config invalid", "err", err)
		os.Exit(1)
	}
	if err := run(cfg, log); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

// newHTTPServer builds the server with full timeouts. ReadTimeout is generous
// (3m) because /admin/media accepts 100MB uploads that must complete on slow
// links; WriteTimeout (1m) covers the largest response this service produces
// (server-rendered pages and 302 redirects); IdleTimeout bounds keep-alives.
func newHTTPServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       3 * time.Minute,
		WriteTimeout:      time.Minute,
		IdleTimeout:       2 * time.Minute,
	}
}

func run(cfg *config.Config, log *slog.Logger) error {
	// DB: auto-migrate, fail fast on migration error (AGENTS §3.4).
	d, err := db.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	st := store.New(d)

	// Idempotent recovery: newsletters stuck in 'sending' revert to draft.
	if n, err := st.RevertSendingToDraft(); err != nil {
		_ = d.Close()
		return err
	} else if n > 0 {
		log.Warn("reverted interrupted sends to draft", "count", n)
	}

	// Mailer: SMTP when configured, dev log mailer otherwise.
	var m mailer.Mailer
	if cfg.SMTPHost != "" {
		m = mailer.NewSMTP(cfg.SMTPHost, cfg.SMTPPort, cfg.SMTPUser, cfg.SMTPPass, cfg.SMTPFrom)
	} else {
		log.Warn("CORE_SMTP_HOST empty: emails will be logged, not sent")
		m = mailer.NewLogMailer(log)
	}

	// Turnstile: enforced when secret set, skipped (WARN) in dev otherwise.
	var v turnstile.Verifier
	if cfg.TurnstileEnabled() {
		v = turnstile.NewHTTP(cfg.TurnstileSecret, "")
	} else {
		log.Warn("CORE_TURNSTILE_SECRET empty: captcha checks skipped (dev mode)")
		v = turnstile.Noop{}
	}

	// R2: media upload + backup degrade gracefully without it.
	var s3c s3store.Client
	if cfg.R2Configured() {
		s3c = s3store.NewR2(cfg.R2Endpoint, cfg.R2KeyID, cfg.R2Secret, cfg.R2Bucket, cfg.R2PublicBase)
	} else {
		log.Warn("CORE_R2_* not set: media upload and backups disabled")
	}

	sender := newsletter.NewSender(st, m, cfg, log)
	api := httpapi.NewServer(httpapi.Deps{Store: st, Mailer: m, Verifier: v, Config: cfg, Logger: log})
	adm := admin.NewHandler(admin.Deps{Store: st, Config: cfg, S3: s3c, Sender: sender, Logger: log})

	mux := http.NewServeMux()
	mux.Handle("/api/", api.Routes())
	mux.Handle("/a/", api.Routes())
	mux.Handle("/healthz", api.Routes())
	mux.Handle("/admin", adm.Routes())
	mux.Handle("/admin/", adm.Routes())

	srv := newHTTPServer(cfg.Addr, mux)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Backup ticker (24h), stops with the process context.
	go backup.New(d, s3c, log).Start(ctx)

	errCh := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", cfg.Addr, "env", cfg.Env)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	log.Info("shutting down")
	shCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shCtx); err != nil {
		log.Error("http shutdown", "err", err)
	}
	api.Close()
	// An in-flight newsletter send gets up to 30s to finish, then we exit
	// anyway — the startup recovery reverts its state to draft for resend.
	if !waitWithTimeout(sender.Wait, 30*time.Second) {
		log.Warn("sender wait exceeded 30s; exiting with a send in flight")
	}
	if err := d.Close(); err != nil {
		log.Error("db close", "err", err)
	}
	log.Info("bye")
	return nil
}

// waitWithTimeout runs wait on a goroutine and reports whether it completed
// within d. On timeout the goroutine is left behind; callers are about to
// exit, so the abandoned work must be crash-safe (it is: send recovery
// reverts to draft on next start).
func waitWithTimeout(wait func(), d time.Duration) bool {
	done := make(chan struct{})
	go func() {
		wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(d):
		return false
	}
}
