package httpapi

import (
	"net/http/httptest"
	"net/netip"
	"testing"
)

func TestClientIPIgnoresXFFFromUntrustedPeer(t *testing.T) {
	srv, _, _ := newTestServer(t) // fixture cfg has no trusted proxies

	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "198.51.100.7:5555"
	req.Header.Set("X-Forwarded-For", "203.0.113.99")

	if got := srv.clientIP(req); got != "198.51.100.7" {
		t.Errorf("clientIP = %q, want RemoteAddr (XFF ignored, no trusted proxies)", got)
	}
}

func TestClientIPHonorsXFFFromTrustedPeer(t *testing.T) {
	srv, _, _ := newTestServer(t)
	srv.cfg.TrustedProxies = []netip.Prefix{netip.MustParsePrefix("198.51.100.0/24")}

	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "198.51.100.7:5555"
	req.Header.Set("X-Forwarded-For", "203.0.113.99, 70.41.3.18")

	if got := srv.clientIP(req); got != "203.0.113.99" {
		t.Errorf("clientIP = %q, want first XFF hop (trusted peer)", got)
	}

	// A peer outside the trusted range does not get to spoof.
	req.RemoteAddr = "203.0.113.1:5555"
	if got := srv.clientIP(req); got != "203.0.113.1" {
		t.Errorf("clientIP = %q, want RemoteAddr (untrusted peer)", got)
	}
}

func TestClientIPNoXFFAlwaysUsesRemoteAddr(t *testing.T) {
	srv, _, _ := newTestServer(t)
	srv.cfg.TrustedProxies = []netip.Prefix{netip.MustParsePrefix("0.0.0.0/0")}
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "192.0.2.10:8080"
	if got := srv.clientIP(req); got != "192.0.2.10" {
		t.Errorf("clientIP = %q, want RemoteAddr", got)
	}
}
