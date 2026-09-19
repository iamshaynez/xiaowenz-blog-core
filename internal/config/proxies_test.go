package config

import (
	"net/netip"
	"testing"
)

func TestTrustedProxiesDefaultEmpty(t *testing.T) {
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.TrustedProxies) != 0 {
		t.Errorf("TrustedProxies = %v, want empty (never trust XFF by default)", cfg.TrustedProxies)
	}
	if cfg.TrustsProxy(netip.MustParseAddr("10.0.0.1")) {
		t.Error("empty list must trust nobody")
	}
}

func TestTrustedProxiesParsing(t *testing.T) {
	t.Setenv("CORE_TRUSTED_PROXIES", "10.0.0.0/8, 192.168.1.1 ,2001:db8::/32")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.TrustedProxies) != 3 {
		t.Fatalf("TrustedProxies = %v, want 3 entries", cfg.TrustedProxies)
	}
	cases := []struct {
		ip   string
		want bool
	}{
		{"10.1.2.3", true},     // in 10.0.0.0/8
		{"192.168.1.1", true},  // bare IP entry
		{"192.168.1.2", false}, // neighbor not listed
		{"8.8.8.8", false},     // unrelated
		{"2001:db8::1", true},  // v6 CIDR
		{"2001:db9::1", false}, // v6 outside
	}
	for _, c := range cases {
		if got := cfg.TrustsProxy(netip.MustParseAddr(c.ip)); got != c.want {
			t.Errorf("TrustsProxy(%s) = %v, want %v", c.ip, got, c.want)
		}
	}
}

func TestTrustedProxiesInvalidEntryFailsLoad(t *testing.T) {
	t.Setenv("CORE_TRUSTED_PROXIES", "10.0.0.0/8,not-an-ip")
	if _, err := Load(); err == nil {
		t.Fatal("invalid CORE_TRUSTED_PROXIES entry must fail Load (fail fast)")
	}
}
