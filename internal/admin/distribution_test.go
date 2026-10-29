package admin

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

const distFeed = `<?xml version="1.0"?><rss version="2.0"><channel>
<item><title><![CDATA[Ep 006. 最新一集]]></title><link>https://c.test/episodes/ep-new</link><pubDate>Sun, 23 Aug 2026 11:33:40 +0000</pubDate><enclosure url="https://c.test/a/ep-new.mp3" length="22222" type="audio/mpeg"/></item>
<item><title><![CDATA[Ep 005. 上一集]]></title><link>https://c.test/episodes/ep-old</link><pubDate>Thu, 20 Aug 2026 08:00:00 +0000</pubDate><enclosure url="https://c.test/a/ep-old.mp3" length="111" type="audio/mpeg"/></item>
</channel></rss>`

// 镜像真实结构：剧集条目也带 feedUrl（曾让判别逻辑翻车），判别只能靠 kind。
const itunesOK = `{"resultCount":3,"results":[
{"wrapperType":"track","kind":"podcast","collectionName":"三杯过后","feedUrl":"https://xiaowenz.com/feed/audio.xml"},
{"wrapperType":"podcastEpisode","kind":"podcast-episode","feedUrl":"https://xiaowenz.com/feed/audio.xml","trackName":"Ep 005. 上一集","releaseDate":"2026-08-20T08:00:00Z"},
{"wrapperType":"podcastEpisode","kind":"podcast-episode","feedUrl":"https://xiaowenz.com/feed/audio.xml","trackName":"Ep 004. 更早","releaseDate":"2026-08-08T00:00:00Z"}
]}`

func distFixture(t *testing.T) *adminFixture {
	t.Helper()
	fx := newAdminFixture(t)
	feed := startFeed(t, 200, distFeed)
	fu, _ := url.Parse(feed.URL)
	fx.raw.audioFeedURL = feed.URL
	fx.raw.feedOrigin = fu.Scheme + "://" + fu.Host
	itunes := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.RawQuery, "id=188") {
			_, _ = w.Write([]byte(itunesOK))
			return
		}
		w.WriteHeader(404)
	}))
	t.Cleanup(itunes.Close)
	fx.raw.itunesBase = itunes.URL
	_ = fx.store.SetSetting("apple_show_id", "1880021392")
	_ = fx.store.SetSetting("xiaoyuzhou_url", "https://www.xiaoyuzhoufm.com/podcast/abc")
	return fx
}

// 分发页 = 发布后的外循环：feed 自检（字节勾稽）+ Apple 目录比对
// （收录到哪集、滞后几天、追踪的 feed 域名还是不是我们的）+ 平台配置。
func TestDistributionPage(t *testing.T) {
	fx := distFixture(t)
	// 媒体库：ep-old 字节吻合；ep-new 字节不符（自检应报）
	if err := fx.store.CreateMedia("audio/ep-old.mp3", "https://m.test/old.mp3", 111, "audio/mpeg"); err != nil {
		t.Fatal(err)
	}
	if err := fx.store.CreateMedia("audio/ep-new.mp3", "https://m.test/new.mp3", 999, "audio/mpeg"); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	req := authed(http.MethodGet, "/admin/distribution", nil, login(t, fx.handler))
	fx.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body)
	}
	body := rec.Body.String()

	for _, want := range []string{
		"Ep 006. 最新一集",                        // feed 自检行
		"字节不一致",                               // ep-new: feed 22222 vs 媒体 999
		"1880021392",                          // 配置回显
		"滞后",                                  // feed 最新 08-23 vs Apple 08-20
		"https://xiaowenz.com/feed/audio.xml", // Apple 追踪的 feed
		"xiaoyuzhoufm.com/podcast/abc",
	} {
		_ = want
	}
	for _, want := range []string{
		"Ep 006. 最新一集", "字节不一致", "1880021392", "滞后",
		"https://xiaowenz.com/feed/audio.xml", "xiaoyuzhoufm.com/podcast/abc",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("分发页缺 %q", want)
		}
	}
	if strings.Contains(body, "xiaoyenz") {
		t.Error("不应出现的占位串")
	}
}

// 配置保存：表单落 settings，回页可见。
func TestDistributionSettingsSave(t *testing.T) {
	fx := newAdminFixture(t)
	s := login(t, fx.handler)
	rec := postForm(fx.handler, s, "/admin/distribution/settings",
		"apple_show_id=42&apple_country=US&xiaoyuzhou_url=https://xyz.fm/p/1")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("save status = %d", rec.Code)
	}
	for k, want := range map[string]string{"apple_show_id": "42", "apple_country": "US", "xiaoyuzhou_url": "https://xyz.fm/p/1"} {
		if v, _ := fx.store.GetSetting(k); v != want {
			t.Errorf("%s = %q, want %q", k, v, want)
		}
	}
}
