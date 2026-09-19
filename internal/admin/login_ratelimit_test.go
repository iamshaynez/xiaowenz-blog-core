package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLoginRateLimitedPerIP(t *testing.T) {
	fx := newAdminFixture(t)

	// 5 attempts per minute per IP are allowed through (wrong ones get 401).
	for i := 0; i < 5; i++ {
		req := httptest.NewRequest(http.MethodPost, "/admin/login", strings.NewReader("token=wrong"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		fx.handler.ServeHTTP(rec, req)
		if rec.Code == http.StatusTooManyRequests {
			t.Fatalf("attempt %d wrongly rate-limited", i+1)
		}
	}

	// 6th attempt from the same IP is rejected before any token comparison.
	req := httptest.NewRequest(http.MethodPost, "/admin/login", strings.NewReader("token=test-admin-token"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	fx.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("6th attempt: status = %d, want 429", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "error") {
		t.Error("429 body must be the standard {\"error\":...} shape")
	}
}

func TestLoginRateLimitIsPerIP(t *testing.T) {
	fx := newAdminFixture(t)
	for i := 0; i < 6; i++ {
		req := httptest.NewRequest(http.MethodPost, "/admin/login", strings.NewReader("token=wrong"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		fx.handler.ServeHTTP(rec, req)
	}
	// A different source IP still gets its own bucket.
	req := httptest.NewRequest(http.MethodPost, "/admin/login", strings.NewReader("token=test-admin-token"))
	req.RemoteAddr = "203.0.113.9:9999"
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	fx.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Errorf("fresh IP: status = %d, want 303 (own bucket)", rec.Code)
	}
}
