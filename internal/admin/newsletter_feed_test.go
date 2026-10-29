package admin

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// startFeed serves a fake blog RSS feed (feed.xml shape: CDATA titles,
// absolute links pointing at the "canonical" apex even though the feed is
// served from another host — exactly what Hugo emits during our staging).
func startFeed(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

const fakeFeed = `<?xml version="1.0"?><rss version="2.0"><channel>
<title>三杯过后</title>
<item><title><![CDATA[每个人都快起来了，公司为什么没快起来？]]></title><link>https://canonical.test/blog/ai-performance.html</link><pubDate>Sun, 23 Aug 2026 11:33:40 +0000</pubDate></item>
<item><title><![CDATA[Ep 006. 买回余闲]]></title><link>https://canonical.test/episodes/freetime</link><pubDate>Thu, 20 Aug 2026 08:00:00 +0000</pubDate></item>
</channel></rss>`

// 「新建 Newsletter」页从博客 feed 拉最近文章渲染成下拉列表：
// 标题可读、链接主机改写为 feed 实际来源（canonical 链接在切换期可能打不开）。
func TestNewsletterNewListsRecentPosts(t *testing.T) {
	fx := newAdminFixture(t)
	feed := startFeed(t, 200, fakeFeed)
	fu, _ := url.Parse(feed.URL)
	fx.raw.feedURL = feed.URL + "/feed.xml"
	fx.raw.feedOrigin = fu.Scheme + "://" + fu.Host // 模拟 CORE_SITE_URL 源

	rec := httptest.NewRecorder()
	req := authed(http.MethodGet, "/admin/newsletters/new", nil, login(t, fx.handler))
	fx.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "每个人都快起来了，公司为什么没快起来？") {
		t.Error("下拉列表缺文章标题")
	}
	if !strings.Contains(body, fu.Host+"/blog/ai-performance.html") {
		t.Errorf("链接主机未改写为 feed 来源: 缺 %s/blog/ai-performance.html", fu.Host)
	}
	if strings.Contains(body, "https://canonical.test/") {
		t.Error("canonical 主机泄漏进页面（切换期点击会 404）")
	}
	if !strings.Contains(body, "2026-08-23") {
		t.Error("标题后缺可读日期")
	}
}

// feed 拉取失败 → 页面照常渲染，手动 URL 输入仍是兜底，绝不 500。
func TestNewsletterNewFeedFailureFallsBack(t *testing.T) {
	fx := newAdminFixture(t)
	feed := startFeed(t, 500, "boom")
	fx.raw.feedURL = feed.URL + "/feed.xml"
	fu, _ := url.Parse(feed.URL)
	fx.raw.feedOrigin = fu.Scheme + "://" + fu.Host

	rec := httptest.NewRecorder()
	req := authed(http.MethodGet, "/admin/newsletters/new", nil, login(t, fx.handler))
	fx.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "/admin/newsletters/import") {
		t.Error("手动导入表单必须保留（兜底路径）")
	}
}

// feed 结果缓存：10 分钟内重复打开页面不重新抓取。
func TestNewsletterFeedCached(t *testing.T) {
	fx := newAdminFixture(t)
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Write([]byte(fakeFeed))
	}))
	t.Cleanup(srv.Close)
	fu, _ := url.Parse(srv.URL)
	fx.raw.feedURL = srv.URL
	fx.raw.feedOrigin = fu.Scheme + "://" + fu.Host

	for i := 0; i < 3; i++ {
		rec := httptest.NewRecorder()
		req := authed(http.MethodGet, "/admin/newsletters/new", nil, login(t, fx.handler))
		fx.handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("render %d: status = %d", i, rec.Code)
		}
	}
	if hits != 1 {
		t.Errorf("feed 抓取次数 = %d, want 1（应命中缓存）", hits)
	}
	// 缓存过期后允许重新抓取。
	fx.raw.mu.Lock()
	fx.raw.fetchedAt = time.Now().Add(-11 * time.Minute)
	fx.raw.mu.Unlock()
	rec := httptest.NewRecorder()
	req := authed(http.MethodGet, "/admin/newsletters/new", nil, login(t, fx.handler))
	fx.handler.ServeHTTP(rec, req)
	if hits != 2 {
		t.Errorf("过期后抓取次数 = %d, want 2", hits)
	}
}
