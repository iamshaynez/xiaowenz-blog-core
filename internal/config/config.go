// Package config loads service configuration from environment variables.
package config

import (
	"fmt"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds all runtime configuration, sourced exclusively from env vars.
type Config struct {
	Addr    string
	DBPath  string
	BaseURL string
	SiteURL string
	Env     string

	AdminToken string

	SMTPHost string
	SMTPPort int
	SMTPUser string
	SMTPPass string
	SMTPFrom string

	R2Endpoint   string
	R2KeyID      string
	R2Secret     string
	R2Bucket     string
	R2PublicBase string

	TurnstileSecret string

	// TrustedProxies are the only peers whose X-Forwarded-For header is
	// honored. Empty (default) = never trust XFF; the peer address is used.
	TrustedProxies []netip.Prefix

	// SendInterval paces newsletter delivery (default 1/sec per SPEC §4).
	SendInterval time.Duration
	// MailingAddress is the physical-address placeholder in newsletter footers.
	MailingAddress string
}

// IsProd reports whether the service runs in production mode.
func (c *Config) IsProd() bool { return c.Env == "prod" }

// R2Configured reports whether the R2/S3 integration is fully configured.
func (c *Config) R2Configured() bool {
	return c.R2Endpoint != "" && c.R2KeyID != "" && c.R2Secret != "" && c.R2Bucket != ""
}

// TurnstileEnabled reports whether Turnstile verification is active.
func (c *Config) TurnstileEnabled() bool { return c.TurnstileSecret != "" }

// TrustsProxy reports whether ip belongs to a configured trusted proxy.
func (c *Config) TrustsProxy(ip netip.Addr) bool {
	for _, p := range c.TrustedProxies {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// Load reads configuration from the environment, applying SPEC §1 defaults,
// and validates prod-mode requirements (fail fast).
func Load() (*Config, error) {
	cfg := &Config{
		Addr:            envOr("CORE_ADDR", ":8080"),
		DBPath:          envOr("CORE_DB_PATH", "/data/core.db"),
		BaseURL:         envOr("CORE_BASE_URL", "http://localhost:8080"),
		SiteURL:         envOr("CORE_SITE_URL", "http://localhost:1313"),
		Env:             envOr("CORE_ENV", "dev"),
		AdminToken:      os.Getenv("CORE_ADMIN_TOKEN"),
		SMTPHost:        os.Getenv("CORE_SMTP_HOST"),
		SMTPUser:        os.Getenv("CORE_SMTP_USER"),
		SMTPPass:        os.Getenv("CORE_SMTP_PASS"),
		SMTPFrom:        os.Getenv("CORE_SMTP_FROM"),
		R2Endpoint:      os.Getenv("CORE_R2_ENDPOINT"),
		R2KeyID:         os.Getenv("CORE_R2_KEY_ID"),
		R2Secret:        os.Getenv("CORE_R2_SECRET"),
		R2Bucket:        os.Getenv("CORE_R2_BUCKET"),
		R2PublicBase:    os.Getenv("CORE_R2_PUBLIC_BASE"),
		TurnstileSecret: os.Getenv("CORE_TURNSTILE_SECRET"),
		MailingAddress:  os.Getenv("CORE_MAILING_ADDRESS"),
		SendInterval:    time.Second,
		SMTPPort:        587,
	}

	if p := os.Getenv("CORE_SMTP_PORT"); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil, fmt.Errorf("CORE_SMTP_PORT: %w", err)
		}
		cfg.SMTPPort = n
	}
	if ms := os.Getenv("CORE_SEND_INTERVAL_MS"); ms != "" {
		n, err := strconv.Atoi(ms)
		if err != nil {
			return nil, fmt.Errorf("CORE_SEND_INTERVAL_MS: %w", err)
		}
		cfg.SendInterval = time.Duration(n) * time.Millisecond
	}

	if v := os.Getenv("CORE_TRUSTED_PROXIES"); v != "" {
		for _, entry := range strings.Split(v, ",") {
			entry = strings.TrimSpace(entry)
			if entry == "" {
				continue
			}
			prefix, err := parseProxyEntry(entry)
			if err != nil {
				return nil, fmt.Errorf("CORE_TRUSTED_PROXIES entry %q: %w", entry, err)
			}
			cfg.TrustedProxies = append(cfg.TrustedProxies, prefix)
		}
	}

	if cfg.IsProd() {
		var missing []string
		if cfg.AdminToken == "" {
			missing = append(missing, "CORE_ADMIN_TOKEN")
		}
		if cfg.SMTPHost == "" || cfg.SMTPFrom == "" {
			missing = append(missing, "CORE_SMTP_HOST/CORE_SMTP_FROM")
		}
		if cfg.BaseURL == "" || cfg.SiteURL == "" {
			missing = append(missing, "CORE_BASE_URL/CORE_SITE_URL")
		}
		if len(missing) > 0 {
			return nil, fmt.Errorf("prod mode requires %v", missing)
		}
	}
	return cfg, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// parseProxyEntry accepts a CIDR or a bare IP (mapped to a host prefix).
func parseProxyEntry(entry string) (netip.Prefix, error) {
	if strings.Contains(entry, "/") {
		return netip.ParsePrefix(entry)
	}
	addr, err := netip.ParseAddr(entry)
	if err != nil {
		return netip.Prefix{}, err
	}
	return netip.PrefixFrom(addr, addr.BitLen()), nil
}
