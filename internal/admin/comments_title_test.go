package admin

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// ── 评论评审展示文章标题（slug 只作次要信息）─────────────────────────
// 标题来自博客/播客 feed 的 slug→title 汇总；老文章不在 feed 里时回退 slug。
func TestCommentsShowPostTitle(t *testing.T) {
	fx := newAdminFixture(t)
	feed := startFeed(t, 200, `<?xml version="1.0"?><rss version="2.0"><channel>
<item><title><![CDATA[每个人都快起来了，公司为什么没快起来？]]></title><link>https://c.test/blog/ai-performance.html</link><pubDate>Sun, 23 Aug 2026 11:33:40 +0000</pubDate></item>
</channel></rss>`)
	fu, err := url.Parse(feed.URL)
	if err != nil {
		t.Fatal(err)
	}
	fx.raw.feedURL = feed.URL
	fx.raw.feedOrigin = fu.Scheme + "://" + fu.Host

	if _, err := fx.store.CreateComment("ai-performance", "读者", "", "好文", "ip", "ua"); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.store.CreateComment("very-old-post", "读者", "", "老文评论", "ip", "ua"); err != nil {
		t.Fatal(err)
	}

	s := login(t, fx.handler)
	rec := httptest.NewRecorder()
	req := authed(http.MethodGet, "/admin/comments", nil, s)
	fx.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "每个人都快起来了，公司为什么没快起来？") {
		t.Error("评审行应展示文章标题（来自博客 feed）")
	}
	if !strings.Contains(body, "very-old-post") {
		t.Error("feed 外的老文章应回退显示 slug")
	}

	// 仪表盘待审预览同样带标题
	dash := httptest.NewRecorder()
	req2 := authed(http.MethodGet, "/admin", nil, s)
	fx.handler.ServeHTTP(dash, req2)
	if !strings.Contains(dash.Body.String(), "每个人都快起来了") {
		t.Error("仪表盘待审预览也应展示标题")
	}
}
