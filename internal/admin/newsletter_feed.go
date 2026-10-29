package admin

import (
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// 博客文章源联动（SPEC §4）：「新建 Newsletter」页从博客 RSS feed
// （{CORE_SITE_URL}/feed.xml，派生自既有配置，不新增 env）拉最近文章
// 渲染成下拉列表，选中即走 /admin/newsletters/import 生成草稿——
// 全程零手动输入；feed 拉不到时页面照常渲染，手动粘 URL 仍是兜底。
//
// 链接主机改写：Hugo feed 里的 <link> 是 baseURL 绝对地址（apex），
// 而 apex 切换前内容实际在 CORE_SITE_URL 指向的主机上——草稿链接统一
// 改写为 feed 实际来源源（feedOrigin），保证点开就能读。

const (
	feedTTL      = 10 * time.Minute
	feedMaxItems = 30
)

type feedPost struct {
	Title  string
	Date   string // YYYY-MM-DD，可读展示用
	URL    string // 已改写为 feedOrigin 的文章链接
	Slug   string // URL 末段（剧集/文章 slug），对账用
	EncLen int    // enclosure length 属性（播客字节勾稽用）
}

var (
	reItem    = regexp.MustCompile(`(?is)<item>(.*?)</item>`)
	reItemTi  = regexp.MustCompile(`(?is)<title>(?:<!\[CDATA\[)?(.*?)(?:\]\]>)?</title>`)
	reItemURL = regexp.MustCompile(`(?is)<link>(.*?)</link>`)
	reItemDt  = regexp.MustCompile(`(?is)<pubDate>(.*?)</pubDate>`)
	reItemEnc = regexp.MustCompile(`(?is)<enclosure[^>]+length="(\d+)"`)
)

// blogPosts returns the cached recent-posts list, refreshing from the blog
// feed when the cache is older than feedTTL. Any failure yields nil — the
// page must degrade to the manual URL form, never 500.
func (h *Handler) blogPosts() []feedPost {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.posts != nil && time.Since(h.fetchedAt) < feedTTL {
		return h.posts
	}
	posts := h.fetchBlogPosts()
	if posts != nil { // 失败时保留旧缓存（如有），下次再试
		h.posts, h.fetchedAt = posts, time.Now()
	}
	return h.posts
}

func (h *Handler) fetchBlogPosts() []feedPost {
	return h.fetchFeedPosts(h.feedURL)
}

// audioFeedPosts returns the cached podcast-feed episode list; ok=false when
// the feed is unreachable (stale cache still counts as ok).
func (h *Handler) audioFeedPosts() ([]feedPost, bool) {
	h.mu.Lock()
	if h.audioPosts != nil && time.Since(h.audioFetchedAt) < feedTTL {
		posts := h.audioPosts
		h.mu.Unlock()
		return posts, true
	}
	h.mu.Unlock()
	posts := h.fetchFeedPosts(h.audioFeedURL)
	if posts == nil {
		h.mu.Lock()
		stale := h.audioPosts
		h.mu.Unlock()
		if stale != nil {
			return stale, true
		}
		return nil, false
	}
	h.mu.Lock()
	h.audioPosts, h.audioFetchedAt = posts, time.Now()
	h.mu.Unlock()
	return posts, true
}

// fetchFeedPosts fetches and parses one RSS feed; any failure yields nil.
func (h *Handler) fetchFeedPosts(feedURL string) []feedPost {
	if feedURL == "" || h.feedOrigin == "" {
		return nil
	}
	resp, err := h.feedClient.Get(feedURL)
	if err != nil {
		h.log.Warn("blog feed fetch", "err", err)
		return nil
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		h.log.Warn("blog feed status", "code", resp.StatusCode)
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil
	}

	var posts []feedPost
	for _, m := range reItem.FindAllSubmatch(body, feedMaxItems) {
		title := submatch(reItemTi, m[1])
		rawURL := submatch(reItemURL, m[1])
		if title == "" || rawURL == "" {
			continue
		}
		u, err := url.Parse(rawURL)
		if err != nil || u.Path == "" {
			continue
		}
		link := h.feedOrigin + u.Path
		if u.RawQuery != "" {
			link += "?" + u.RawQuery
		}
		date := ""
		if ds := submatch(reItemDt, m[1]); ds != "" {
			if t, err := time.Parse(time.RFC1123Z, ds); err == nil {
				date = t.Local().Format("2006-01-02")
			}
		}
		slug := strings.Trim(u.Path, "/")
		if i := strings.LastIndex(slug, "/"); i >= 0 {
			slug = slug[i+1:]
		}
		slug = strings.TrimSuffix(slug, ".html") // Hugo 博客是 /blog/{slug}..html
		encLen := 0
		if em := reItemEnc.FindSubmatch(m[1]); em != nil {
			encLen, _ = strconv.Atoi(string(em[1]))
		}
		posts = append(posts, feedPost{Title: title, Date: date, URL: link, Slug: slug, EncLen: encLen})
	}
	if len(posts) == 0 {
		return nil
	}
	return posts
}

// submatch returns the trimmed first capture of re in b, or "".
func submatch(re *regexp.Regexp, b []byte) string {
	m := re.FindSubmatch(b)
	if m == nil {
		return ""
	}
	return strings.TrimSpace(string(m[1]))
}

// slugTitles 汇总博客与播客 feed 的 slug→标题，供评论评审等处展示；
// feed 拉不到时返回空 map（调用方回退 slug）。
func (h *Handler) slugTitles() map[string]string {
	m := map[string]string{}
	if posts, ok := h.audioFeedPosts(); ok {
		for _, p := range posts {
			m[p.Slug] = p.Title
		}
	}
	for _, p := range h.blogPosts() {
		if _, exists := m[p.Slug]; !exists {
			m[p.Slug] = p.Title
		}
	}
	return m
}
