package config

import (
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	t.Setenv("CORE_ADDR", "")
	t.Setenv("CORE_DB_PATH", "")
	t.Setenv("CORE_BASE_URL", "")
	t.Setenv("CORE_SITE_URL", "")
	t.Setenv("CORE_ENV", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() with defaults returned error: %v", err)
	}
	if cfg.Addr != ":8080" {
		t.Errorf("Addr = %q, want :8080", cfg.Addr)
	}
	if cfg.DBPath != "/data/core.db" {
		t.Errorf("DBPath = %q, want /data/core.db", cfg.DBPath)
	}
	if cfg.BaseURL != "http://localhost:8080" {
		t.Errorf("BaseURL = %q, want http://localhost:8080", cfg.BaseURL)
	}
	if cfg.SiteURL != "http://localhost:1313" {
		t.Errorf("SiteURL = %q, want http://localhost:1313", cfg.SiteURL)
	}
	if cfg.Env != "dev" {
		t.Errorf("Env = %q, want dev", cfg.Env)
	}
	if cfg.SendInterval != time.Second {
		t.Errorf("SendInterval = %v, want 1s", cfg.SendInterval)
	}
	if cfg.SMTPPort != 587 {
		t.Errorf("SMTPPort = %d, want 587", cfg.SMTPPort)
	}
	if cfg.R2Configured() {
		t.Error("R2Configured() = true with empty R2 env, want false")
	}
	if cfg.TurnstileEnabled() {
		t.Error("TurnstileEnabled() = true with empty secret, want false")
	}
}

func TestLoadFromEnv(t *testing.T) {
	t.Setenv("CORE_ADDR", ":9090")
	t.Setenv("CORE_DB_PATH", "/tmp/x.db")
	t.Setenv("CORE_BASE_URL", "https://core.example.com")
	t.Setenv("CORE_SITE_URL", "https://example.com")
	t.Setenv("CORE_ADMIN_TOKEN", "secret")
	t.Setenv("CORE_SMTP_HOST", "smtp.example.com")
	t.Setenv("CORE_SMTP_PORT", "465")
	t.Setenv("CORE_SMTP_USER", "u")
	t.Setenv("CORE_SMTP_PASS", "p")
	t.Setenv("CORE_SMTP_FROM", "Letter <letter@example.com>")
	t.Setenv("CORE_R2_ENDPOINT", "https://r2.example.com")
	t.Setenv("CORE_R2_KEY_ID", "kid")
	t.Setenv("CORE_R2_SECRET", "sec")
	t.Setenv("CORE_R2_BUCKET", "bucket")
	t.Setenv("CORE_R2_PUBLIC_BASE", "https://media.example.com")
	t.Setenv("CORE_TURNSTILE_SECRET", "ts")
	t.Setenv("CORE_ENV", "prod")
	t.Setenv("CORE_SEND_INTERVAL_MS", "250")
	t.Setenv("CORE_MAILING_ADDRESS", "Somewhere 1")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.Addr != ":9090" || cfg.DBPath != "/tmp/x.db" {
		t.Errorf("Addr/DBPath not loaded: %q %q", cfg.Addr, cfg.DBPath)
	}
	if cfg.SMTPPort != 465 {
		t.Errorf("SMTPPort = %d, want 465", cfg.SMTPPort)
	}
	if cfg.SendInterval != 250*time.Millisecond {
		t.Errorf("SendInterval = %v, want 250ms", cfg.SendInterval)
	}
	if !cfg.R2Configured() {
		t.Error("R2Configured() = false, want true")
	}
	if !cfg.TurnstileEnabled() {
		t.Error("TurnstileEnabled() = false, want true")
	}
	if !cfg.IsProd() {
		t.Error("IsProd() = false, want true")
	}
	if cfg.MailingAddress != "Somewhere 1" {
		t.Errorf("MailingAddress = %q", cfg.MailingAddress)
	}
}

func TestProdValidationFailsFast(t *testing.T) {
	// prod without ADMIN_TOKEN/SMTP/URLs must be rejected.
	t.Setenv("CORE_ENV", "prod")
	t.Setenv("CORE_ADMIN_TOKEN", "")
	t.Setenv("CORE_SMTP_HOST", "")
	t.Setenv("CORE_SMTP_FROM", "")
	t.Setenv("CORE_BASE_URL", "")
	t.Setenv("CORE_SITE_URL", "")

	if _, err := Load(); err == nil {
		t.Fatal("Load() in prod with missing secrets must fail")
	}
}

func TestProdValidationOK(t *testing.T) {
	t.Setenv("CORE_ENV", "prod")
	t.Setenv("CORE_ADMIN_TOKEN", "tok")
	t.Setenv("CORE_SMTP_HOST", "smtp.example.com")
	t.Setenv("CORE_SMTP_FROM", "a@b.c")
	t.Setenv("CORE_BASE_URL", "https://core.example.com")
	t.Setenv("CORE_SITE_URL", "https://example.com")

	if _, err := Load(); err != nil {
		t.Fatalf("Load() in prod with all required vars: %v", err)
	}
}

func TestInvalidPortRejected(t *testing.T) {
	t.Setenv("CORE_SMTP_PORT", "not-a-number")
	if _, err := Load(); err == nil {
		t.Fatal("Load() with non-numeric CORE_SMTP_PORT must fail")
	}
}
