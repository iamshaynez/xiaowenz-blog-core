package httpapi

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// oversizedBody returns a >1MiB JSON-looking payload.
func oversizedBody() *bytes.Reader {
	// 1MiB of padding inside a JSON field pushes the body over the cap.
	return bytes.NewReader([]byte(`{"email":"a@b.com","content":"` + strings.Repeat("x", 1<<20+100) + `"}`))
}

func TestSubscribeRejectsOversizedBody(t *testing.T) {
	srv, _, _ := newTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/subscribe", oversizedBody())
	rec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized subscribe body: status = %d, want 413", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"error"`) {
		t.Error("413 body must use the standard {\"error\":...} shape")
	}
}

func TestCommentRejectsOversizedBody(t *testing.T) {
	srv, _, _ := newTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/comments", oversizedBody())
	rec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized comment body: status = %d, want 413", rec.Code)
	}
}

func TestPageviewRejectsOversizedBody(t *testing.T) {
	srv, _, _ := newTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/pv", oversizedBody())
	rec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized pv body: status = %d, want 413", rec.Code)
	}
}

func TestNormalSizedBodyStillAccepted(t *testing.T) {
	srv, _, _ := newTestServer(t)
	rec := doJSON(t, srv, http.MethodPost, "/api/pv", map[string]string{"path": "/ok"}, nil)
	if rec.Code != http.StatusNoContent {
		t.Errorf("normal pv: status = %d, want 204", rec.Code)
	}
}
