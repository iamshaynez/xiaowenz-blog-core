package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPageviewRecorded(t *testing.T) {
	srv, st, _ := newTestServer(t)
	rec := doJSON(t, srv, http.MethodPost, "/api/pv",
		map[string]string{"path": "/posts/hello", "referrer": "https://google.com"}, nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	pv, uv, err := st.PageviewStats(0)
	if err != nil {
		t.Fatalf("PageviewStats: %v", err)
	}
	if pv != 1 || uv != 1 {
		t.Errorf("pv=%d uv=%d, want 1/1", pv, uv)
	}
}

func TestPageviewRespectsDNT(t *testing.T) {
	srv, st, _ := newTestServer(t)
	rec := doJSON(t, srv, http.MethodPost, "/api/pv",
		map[string]string{"path": "/x"}, map[string]string{"DNT": "1"})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("DNT status = %d, want 204 (silent drop)", rec.Code)
	}
	pv, _, _ := st.PageviewStats(0)
	if pv != 0 {
		t.Error("DNT request must not be recorded")
	}
}

func TestPageviewRateLimited(t *testing.T) {
	srv, _, _ := newTestServer(t)
	srv.pvLimit = newTestLimiter(60)
	for i := 0; i < 60; i++ {
		rec := doJSON(t, srv, http.MethodPost, "/api/pv", map[string]string{"path": "/x"}, nil)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("request %d: %d", i+1, rec.Code)
		}
	}
	rec := doJSON(t, srv, http.MethodPost, "/api/pv", map[string]string{"path": "/x"}, nil)
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("61st pv: %d, want 429", rec.Code)
	}
}

func TestPageviewRequiresPath(t *testing.T) {
	srv, _, _ := newTestServer(t)
	rec := doJSON(t, srv, http.MethodPost, "/api/pv", map[string]string{"path": ""}, nil)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("empty path: %d, want 400", rec.Code)
	}
}

func TestDownloadMiss404(t *testing.T) {
	srv, _, _ := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/a/none.mp3", nil)
	rec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
	var resp map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp["error"] == "" {
		t.Error("404 body must be {\"error\":...}")
	}
}

func TestDownloadRedirectAndSameDayDedupe(t *testing.T) {
	srv, st, _ := newTestServer(t)
	if err := st.CreateMedia("audio/ep7.mp3", "https://media.test/audio/ep7.mp3", 100, "audio/mpeg"); err != nil {
		t.Fatalf("seed media: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/a/ep7.mp3", nil)
	rec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "https://media.test/audio/ep7.mp3" {
		t.Errorf("Location = %q", loc)
	}

	stats, err := st.DownloadStats(0)
	if err != nil {
		t.Fatalf("DownloadStats: %v", err)
	}
	if stats["ep7"] != 1 {
		t.Errorf("downloads = %v, want ep7=1", stats)
	}

	// Range request same day from same IP: redirects again, counts once.
	req = httptest.NewRequest(http.MethodGet, "/a/ep7.mp3", nil)
	req.Header.Set("Range", "bytes=0-99")
	rec = httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusFound {
		t.Errorf("range request: %d, want 302", rec.Code)
	}
	stats, _ = st.DownloadStats(0)
	if stats["ep7"] != 1 {
		t.Errorf("same-day repeat counted twice: %v", stats)
	}
}

func TestHealthz(t *testing.T) {
	srv, _, _ := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var resp map[string]bool
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp["ok"] || !resp["db"] {
		t.Errorf("healthz = %v", resp)
	}
	if !strings.Contains(rec.Header().Get("Content-Type"), "application/json") {
		t.Error("healthz must be JSON")
	}
}
