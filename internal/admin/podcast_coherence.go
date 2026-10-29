package admin

import (
	"strconv"
	"strings"

	"github.com/iamshaynez/xiaowenz-blog-core/internal/store"
)

// mediaItem 保持与 store.Media 同构，避免对账层直接依赖 store 细节。
type mediaItem = store.Media

// 播客对账（SPEC §4）：媒体库 audio/* 与播客 feed（{CORE_SITE_URL}/feed/audio.xml）
// 的剧集互相勾稽，媒体页直接回答三个问题：
//
//	已上线 —— 音频已传、剧集已发（正常态）
//	未上线 —— 音频已传、剧集 md 还没发；附可抄的 frontmatter 片段（含真实字节数）
//	缺音频 —— feed 剧集指向未上传的音频；此时 enclosure 会 404，必须先补传
//
// feed 拉不到时对账区标注不可用，页面绝不 500。

type podcastRow struct {
	Slug    string
	Bytes   int64
	Live    bool
	Snippet string // 未上线时给作者抄的剧集 frontmatter 片段
}

type podcastCoherence struct {
	Rows    []podcastRow
	Missing []feedPost // feed 里有、媒体库缺音频的剧集（enclosure 会 404）
	FeedOK  bool
}

// podcastCoherenceFor diffs uploaded audio media against the podcast feed.
// media is the caller's already-fetched media list (mediaPage needs it too).
func (h *Handler) podcastCoherenceFor(media []mediaItem) *podcastCoherence {
	posts, ok := h.audioFeedPosts()
	slugLive := map[string]bool{}
	for _, p := range posts {
		slugLive[p.Slug] = true
	}

	c := &podcastCoherence{FeedOK: ok}
	uploaded := map[string]bool{}
	for _, m := range media {
		if !strings.HasPrefix(m.Key, "audio/") || !strings.HasSuffix(m.Key, ".mp3") {
			continue
		}
		slug := strings.TrimSuffix(strings.TrimPrefix(m.Key, "audio/"), ".mp3")
		uploaded[slug] = true
		row := podcastRow{Slug: slug, Bytes: m.Bytes, Live: ok && slugLive[slug]}
		if !row.Live {
			row.Snippet = h.episodeSnippet(slug, m.Bytes)
		}
		c.Rows = append(c.Rows, row)
	}
	for _, p := range posts {
		if !uploaded[p.Slug] {
			c.Missing = append(c.Missing, p)
		}
	}
	return c
}

// episodeSnippet 生成剧集 md 的 frontmatter 片段（字节真实、slug 对齐媒体库）。
func (h *Handler) episodeSnippet(slug string, bytes int64) string {
	img := ""
	if h.cfg.R2PublicBase != "" {
		img = "\naudio_image: \"" + h.cfg.R2PublicBase + "/images/episode-ep001.jpg\""
	}
	return "---\ntitle: \"\"\ntype: episode\naudio_slug: " + slug +
		"\naudio_bytes: " + strconv.FormatInt(bytes, 10) + img + "\ndraft: false\n---\n\n（shownotes 正文）"
}
