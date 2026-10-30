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

	"github.com/iamshaynez/xiaowenz-blog-core/internal/admin"
	"github.com/iamshaynez/xiaowenz-blog-core/internal/backup"
	"github.com/iamshaynez/xiaowenz-blog-core/internal/config"
	"github.com/iamshaynez/xiaowenz-blog-core/internal/db"
	"github.com/iamshaynez/xiaowenz-blog-core/internal/httpapi"
	"github.com/iamshaynez/xiaowenz-blog-core/internal/mailer"
	"github.com/iamshaynez/xiaowenz-blog-core/internal/newsletter"
	"github.com/iamshaynez/xiaowenz-blog-core/internal/s3store"
	"github.com/iamshaynez/xiaowenz-blog-core/internal/store"
	"github.com/iamshaynez/xiaowenz-blog-core/internal/sysconfig"
	"github.com/iamshaynez/xiaowenz-blog-core/internal/turnstile"
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

	// R2 targets are console-managed (settings table, admin「系统参数」page):
	// media falls back field-by-field to CORE_R2_* env so existing deployments
	// keep working until the operator moves values into the console; backup
	// has no env fallback (env points at the public media bucket). Clients
	// are rebuilt per operation — console edits apply without a restart.
	envMedia := sysconfig.MediaR2{
		Endpoint:   cfg.R2Endpoint,
		KeyID:      cfg.R2KeyID,
		Secret:     cfg.R2Secret,
		Bucket:     cfg.R2Bucket,
		PublicBase: cfg.R2PublicBase,
	}
	mediaClient := sysconfig.MediaClientFactory(st, envMedia, func(c sysconfig.MediaR2) s3store.Client {
		return s3store.NewR2(c.Endpoint, c.KeyID, c.Secret, c.Bucket, c.PublicBase)
	})

	// Backups: cron schedule and R2 target both come from the settings table
	// (admin console「备份」page); the client is rebuilt per operation, so
	// console edits apply without a restart.
	backer := backup.New(d, log)
	backer.Client = sysconfig.BackupClientFactory(st, func(c sysconfig.BackupR2) s3store.Client {
		return s3store.NewR2(c.Endpoint, c.KeyID, c.Secret, c.Bucket, "")
	})
	backer.GetCron = func() string {
		v, _ := st.GetSetting(backup.SettingKey)
		return v
	}
	backer.Meta = func() map[string]any {
		dbMedia, _ := sysconfig.LoadMediaR2(st)
		eff := sysconfig.EffectiveMediaR2(dbMedia, envMedia)
		r2Backup, _ := sysconfig.LoadBackupR2(st)
		return map[string]any{
			"env":      cfg.Env,
			"base_url": cfg.BaseURL,
			"site_url": cfg.SiteURL,
			"addr":     cfg.Addr,
			// Secrets never leave the process: presence flags only.
			"smtp_configured":      cfg.SMTPHost != "",
			"turnstile_enabled":    cfg.TurnstileEnabled(),
			"r2_media_bucket":      eff.Bucket,
			"media_public_base":    eff.PublicBase,
			"r2_backup_configured": r2Backup.Complete(),
			"r2_backup_bucket":     r2Backup.Bucket,
			"send_interval_ms":     cfg.SendInterval.Milliseconds(),
			"mailing_address":      cfg.MailingAddress,
		}
	}
	if dbMedia, err := sysconfig.LoadMediaR2(st); err == nil {
		if eff := sysconfig.EffectiveMediaR2(dbMedia, envMedia); !eff.Complete() {
			log.Warn("media R2 incomplete (console + env): media upload disabled")
		} else if dbMedia.Complete() {
			log.Info("media R2 active from console settings", "bucket", eff.Bucket)
		} else {
			log.Info("media R2 active from environment fallback", "bucket", eff.Bucket,
				"hint", "configure in admin console to override")
		}
	}
	if !backer.Configured() {
		log.Warn("backup R2 not configured in console: backups disabled until set in admin「系统参数」page")
	}

	sender := newsletter.NewSender(st, m, cfg, log)
	api := httpapi.NewServer(httpapi.Deps{Store: st, Mailer: m, Verifier: v, Config: cfg, Logger: log})
	adm := admin.NewHandler(admin.Deps{Store: st, Config: cfg, MediaClient: mediaClient, Sender: sender, Backups: backer, Logger: log})

	mux := http.NewServeMux()
	mux.Handle("/api/", api.Routes())
	mux.Handle("/a/", api.Routes())
	mux.Handle("/healthz", api.Routes())
	mux.Handle("/admin", adm.Routes())
	mux.Handle("/admin/", adm.Routes())

	srv := newHTTPServer(cfg.Addr, mux)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Backup scheduler (cron from settings), stops with the process context.
	go backer.Start(ctx)

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
