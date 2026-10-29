package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const fakeAudioFeed = `<?xml version="1.0"?><rss version="2.0"><channel>
<item><title><![CDATA[Ep 100. 已上线的剧集]]></title><link>https://canonical.test/episodes/ep-a</link><pubDate>Sun, 23 Aug 2026 11:33:40 +0000</pubDate></item>
<item><title><![CDATA[Ep 101. 缺音频的剧集]]></title><link>https://canonical.test/episodes/ep-c</link><pubDate>Thu, 20 Aug 2026 08:00:00 +0000</pubDate></item>
</channel></rss>`

// 播客对账：媒体库里已传的 audio/* 与播客 feed 的剧集互相勾稽——
// 已上线（feed 有对应剧集）/ 未上线（音频在、剧集 md 还没发，给可抄的
// frontmatter 片段）/ 缺音频（feed 剧集指向未上传的音频，enclosure 会 404）。
func TestMediaPagePodcastCoherence(t *testing.T) {
	fx := newAdminFixture(t)
	if err := fx.store.CreateMedia("audio/ep-a.mp3", "https://media.test/audio/ep-a.mp3", 111, "audio/mpeg"); err != nil {
		t.Fatal(err)
	}
	if err := fx.store.CreateMedia("audio/ep-b.mp3", "https://media.test/audio/ep-b.mp3", 22222, "audio/mpeg"); err != nil {
		t.Fatal(err)
	}
	feed := startFeed(t, 200, fakeAudioFeed)
	fx.raw.audioFeedURL = feed.URL

	rec := httptest.NewRecorder()
	req := authed(http.MethodGet, "/admin/media", nil, login(t, fx.handler))
	fx.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()

	for _, want := range []string{
		"已上线",            // ep-a：音频 + 剧集都在
		"Ep 100. 已上线的剧集", // 已上线行要展示 feed 里的剧集标题，不只是 slug
		"未上线",            // ep-b：音频在、剧集没发
		"剧集未发布",          // 未上线行没有标题可循，应标注而不是空白
		"22222",          // ep-b 的 frontmatter 片段必须带真实字节数
		"audio_slug: ep-b",
		"缺音频",    // ep-c：剧集在 feed 里、音频没传
		"Ep 101", // 缺音频的剧集名要能认出来
	} {
		if !strings.Contains(body, want) {
			t.Errorf("对账视图缺 %q", want)
		}
	}
}

// feed 拉不到 → 媒体页照常渲染（对账区标不可用），绝不 500。
func TestMediaPageFeedFailureStillRenders(t *testing.T) {
	fx := newAdminFixture(t)
	feed := startFeed(t, 404, "nope")
	fx.raw.audioFeedURL = feed.URL

	rec := httptest.NewRecorder()
	req := authed(http.MethodGet, "/admin/media", nil, login(t, fx.handler))
	fx.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "暂无") && !strings.Contains(rec.Body.String(), "不可用") {
		t.Error("feed 失败时对账区应标注不可用或为空")
	}
}
