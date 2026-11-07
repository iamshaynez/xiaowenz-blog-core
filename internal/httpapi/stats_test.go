package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/iamshaynez/xiaowenz-blog-core/internal/sysconfig"
)

// 博客本站打点：Origin 与 CORE_SITE_URL 同域 → 记入 site=”（存量连续）。
func TestPageviewRecorded(t *testing.T) {
	srv, st, _ := newTestServer(t)
	rec := doJSON(t, srv, http.MethodPost, "/api/pv",
		map[string]string{"path": "/posts/hello", "referrer": "https://google.com"},
		map[string]string{"Origin": "https://site.test"})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	pv, uv, err := st.PageviewStats("", 0)
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
		map[string]string{"path": "/x"},
		map[string]string{"DNT": "1", "Origin": "https://site.test"})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("DNT status = %d, want 204 (silent drop)", rec.Code)
	}
	pv, _, _ := st.PageviewStats("", 0)
	if pv != 0 {
		t.Error("DNT request must not be recorded")
	}
}

// 限流在 Origin 门禁之前：未知来源的轰炸同样吃 429，不给攻击者免费额度。
func TestPageviewRateLimited(t *testing.T) {
	srv, _, _ := newTestServer(t)
	srv.pvLimit = newTestLimiter(60)
	for i := 0; i < 60; i++ {
		rec := doJSON(t, srv, http.MethodPost, "/api/pv", map[string]string{"path": "/x"},
			map[string]string{"Origin": "https://site.test"})
		if rec.Code != http.StatusNoContent {
			t.Fatalf("request %d: %d", i+1, rec.Code)
		}
	}
	rec := doJSON(t, srv, http.MethodPost, "/api/pv", map[string]string{"path": "/x"},
		map[string]string{"Origin": "https://site.test"})
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("61st pv: %d, want 429", rec.Code)
	}
}

func TestPageviewRequiresPath(t *testing.T) {
	srv, _, _ := newTestServer(t)
	rec := doJSON(t, srv, http.MethodPost, "/api/pv", map[string]string{"path": ""},
		map[string]string{"Origin": "https://site.test"})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("empty path: %d, want 400", rec.Code)
	}
}

// ── 多站打点：sendBeacon 形态 + Origin 门禁 ─────────────────────────

// doBeacon 模拟 navigator.sendBeacon：text/plain 文本体（简单请求，无预检）。
func doBeacon(t *testing.T, srv *Server, body, origin string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/pv", strings.NewReader(body))
	req.Header.Set("Content-Type", "text/plain;charset=UTF-8")
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	rec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, req)
	return rec
}

func TestPageviewBeaconRegisteredSite(t *testing.T) {
	srv, st, _ := newTestServer(t)
	if err := sysconfig.SaveStatsSites(st, []string{"ask.xiaowenz.com", "card.xiaowenz.com"}); err != nil {
		t.Fatalf("SaveStatsSites: %v", err)
	}

	rec := doBeacon(t, srv, `{"path":"/q/1","referrer":"https://g.com"}`, "https://ask.xiaowenz.com")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("beacon status = %d, want 204", rec.Code)
	}
	pv, uv, err := st.PageviewStats("ask.xiaowenz.com", 0)
	if err != nil || pv != 1 || uv != 1 {
		t.Errorf("ask stats = %d/%d err=%v, want 1/1", pv, uv, err)
	}
	// 他站打点不得混入博客口径。
	if pv, _, _ := st.PageviewStats("", 0); pv != 0 {
		t.Errorf("blog pv = %d, want 0（site 隔离）", pv)
	}
}

// 未注册的 Origin：204 静默丢弃（对方无感、不落库、不污染）。
func TestPageviewUnknownOriginDiscarded(t *testing.T) {
	srv, st, _ := newTestServer(t)
	rec := doBeacon(t, srv, `{"path":"/x"}`, "https://evil.example.com")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("unknown origin status = %d, want 204", rec.Code)
	}
	if pv, _, _ := st.PageviewStats("", 0); pv != 0 {
		t.Errorf("blog pv = %d, want 0", pv)
	}
	if pv, _, _ := st.PageviewStats("evil.example.com", 0); pv != 0 {
		t.Errorf("evil pv = %d, want 0", pv)
	}
}

// 无 Origin（curl / 非浏览器）：丢弃——跨域 POST 浏览器必带 Origin。
func TestPageviewNoOriginDiscarded(t *testing.T) {
	srv, st, _ := newTestServer(t)
	rec := doBeacon(t, srv, `{"path":"/x"}`, "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("no-origin status = %d, want 204", rec.Code)
	}
	if pv, _, _ := st.PageviewStats("", 0); pv != 0 {
		t.Errorf("pv = %d, want 0", pv)
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
		t.Errorf("range request: %d", rec.Code)
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
