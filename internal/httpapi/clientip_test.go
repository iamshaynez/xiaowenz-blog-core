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

	if got := srv.clientIP(req); got != "70.41.3.18" {
		t.Errorf("clientIP = %q, want rightmost XFF hop appended by the trusted peer", got)
	}

	// A peer outside the trusted range does not get to spoof.
	req.RemoteAddr = "203.0.113.1:5555"
	if got := srv.clientIP(req); got != "203.0.113.1" {
		t.Errorf("clientIP = %q, want RemoteAddr (untrusted peer)", got)
	}
}

// A client can prepend arbitrary XFF entries; the trusted proxy appends the
// client's real address last. Taking any leftmost entry would let the client
// rotate fake IPs and evade every per-IP rate limit, so the walk must start
// at the right and skip only trusted proxies.
func TestClientIPSkipsClientPrependedXFFSpoofs(t *testing.T) {
	srv, _, _ := newTestServer(t)
	srv.cfg.TrustedProxies = []netip.Prefix{netip.MustParsePrefix("198.51.100.0/24")}

	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "198.51.100.7:5555"
	req.Header.Set("X-Forwarded-For", "1.2.3.4, 5.6.7.8, 70.41.3.18")

	if got := srv.clientIP(req); got != "70.41.3.18" {
		t.Errorf("clientIP = %q, want 70.41.3.18 (spoofed prefixes must be ignored)", got)
	}

	// Chained trusted proxies: skip them too, land on the first untrusted hop.
	req.Header.Set("X-Forwarded-For", "70.41.3.18, 198.51.100.9, 198.51.100.8")
	if got := srv.clientIP(req); got != "70.41.3.18" {
		t.Errorf("clientIP = %q, want 70.41.3.18 (trusted hops skipped)", got)
	}

	// Garbage entries left of the appended address must not win.
	req.Header.Set("X-Forwarded-For", "not-an-ip, 70.41.3.18")
	if got := srv.clientIP(req); got != "70.41.3.18" {
		t.Errorf("clientIP = %q, want 70.41.3.18 (malformed entries skipped)", got)
	}
}

// When every XFF entry is a trusted proxy the chain never left our own infra;
// fall back to the peer address rather than trusting any hop's claim.
func TestClientIPAllHopsTrustedFallsBackToPeer(t *testing.T) {
	srv, _, _ := newTestServer(t)
	srv.cfg.TrustedProxies = []netip.Prefix{netip.MustParsePrefix("198.51.100.0/24")}

	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "198.51.100.7:5555"
	req.Header.Set("X-Forwarded-For", "198.51.100.9, 198.51.100.8")

	if got := srv.clientIP(req); got != "198.51.100.7" {
		t.Errorf("clientIP = %q, want RemoteAddr when all hops are trusted", got)
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
