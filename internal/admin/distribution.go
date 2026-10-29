package admin

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// 分发管理（SPEC §4）：发布后的外循环。播客平台（Apple/小宇宙）通过
// 抓取我们的 feed/audio.xml 同步，本页回答三件事：
//
//	feed 自检 —— 逐集勾稽 enclosure 字节数 ↔ 媒体库（抄错即暴露）
//	Apple 目录 —— iTunes 公开 lookup API：收录到哪集、滞后几天、
//	              追踪的 feed URL 域名还是不是我们的（迁移哨兵）
//	小宇宙     —— 无公开 API：配置节目页链接 + feed 地址复制 + 机制说明
//
// 配置（apple_show_id / apple_country / xiaoyuzhou_url）存 settings 表。

type feedCheckRow struct {
	Title      string
	Slug       string
	FeedBytes  int
	MediaBytes int64
	MediaOK    bool // 媒体库有注册
	BytesOK    bool // feed enclosure 字节数与媒体库一致
}

type appleStatus struct {
	Configured   bool
	ShowName     string
	FeedURL      string // Apple 目录里登记的 feed 地址
	FeedDomainOK bool   // 是否仍指向本站域名（迁移哨兵）
	LatestTitle  string // Apple 已收录的最新一集
	LatestDate   string // YYYY-MM-DD
	OurLatest    string // 我们 feed 的最新一集日期
	LagDays      int    // >0 = Apple 落后
	Synced       bool
	Err          string
}

func (h *Handler) distributionPage(w http.ResponseWriter, r *http.Request) {
	appleID, _ := h.store.GetSetting("apple_show_id")
	appleCountry, _ := h.store.GetSetting("apple_country")
	if appleCountry == "" {
		appleCountry = "US"
	}
	xyzURL, _ := h.store.GetSetting("xiaoyuzhou_url")

	// feed 自检：audio feed ↔ 媒体库字节勾稽
	posts, feedOK := h.audioFeedPosts()
	mediaBytes := map[string]int64{}
	mediaReg := map[string]bool{}
	if media, err := h.store.ListMedia(); err == nil {
		for _, m := range media {
			if strings.HasPrefix(m.Key, "audio/") && strings.HasSuffix(m.Key, ".mp3") {
				slug := strings.TrimSuffix(strings.TrimPrefix(m.Key, "audio/"), ".mp3")
				mediaBytes[slug] = m.Bytes
				mediaReg[slug] = true
			}
		}
	}
	var checks []feedCheckRow
	for _, p := range posts {
		row := feedCheckRow{Title: p.Title, Slug: p.Slug, FeedBytes: p.EncLen,
			MediaBytes: mediaBytes[p.Slug], MediaOK: mediaReg[p.Slug]}
		row.BytesOK = row.MediaOK && row.FeedBytes > 0 && int64(row.FeedBytes) == row.MediaBytes
		checks = append(checks, row)
	}

	apple := h.appleStatus(appleID, appleCountry, posts)

	h.render(w, "distribution.html", map[string]any{
		"Title":        "分发与同步",
		"AppleID":      appleID,
		"AppleCountry": appleCountry,
		"XyzURL":       xyzURL,
		"FeedOK":       feedOK,
		"Checks":       checks,
		"OurFeedURL":   h.audioFeedURL,
		"Apple":        apple,
		"SiteHostHint": siteHostSuffix(h.cfg.SiteURL),
	})
}

// appleStatus 查询 iTunes 公开目录并对比同步状态；任何失败降级为 Err 文案。
func (h *Handler) appleStatus(showID, country string, posts []feedPost) appleStatus {
	st := appleStatus{Configured: showID != ""}
	if !st.Configured {
		return st
	}
	resp, err := h.importClient.Get(
		h.itunesBase + "/lookup?id=" + urlQueryEscape(showID) +
			"&entity=podcastEpisode&limit=10&country=" + urlQueryEscape(country))
	if err != nil {
		st.Err = "目录查询失败：" + err.Error()
		return st
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		st.Err = "目录查询返回 " + strconv.Itoa(resp.StatusCode)
		return st
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	var lr struct {
		Results []struct {
			WrapperType  string `json:"wrapperType"`
			Kind         string `json:"kind"`
			TrackName    string `json:"trackName"`
			ReleaseDate  string `json:"releaseDate"`
			FeedURL      string `json:"feedUrl"`
			CollectionNm string `json:"collectionName"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &lr); err != nil || len(lr.Results) == 0 {
		st.Err = "目录无此节目（show ID 或区域不对？）"
		return st
	}

	ourLatest := ""
	for _, p := range posts {
		if p.Date > ourLatest {
			ourLatest = p.Date
		}
	}
	st.OurLatest = ourLatest

	for _, r := range lr.Results {
		// 真实响应里剧集条目也带 feedUrl——判别只能靠 wrapperType：
		// kind=podcast 是节目本体，podcastEpisode 是剧集。
		switch r.Kind {
		case "podcast":
			st.ShowName = r.CollectionNm
			st.FeedURL = r.FeedURL
			host := hostOf(r.FeedURL)
			st.FeedDomainOK = host != "" &&
				(host == siteHostSuffix(h.cfg.SiteURL) ||
					strings.HasSuffix(host, "."+siteHostSuffix(h.cfg.SiteURL)))
		case "podcast-episode":
			if len(r.ReleaseDate) >= 10 && r.ReleaseDate[:10] > st.LatestDate {
				st.LatestDate = r.ReleaseDate[:10]
				st.LatestTitle = r.TrackName
			}
		}
	}
	if st.LatestDate != "" && ourLatest != "" {
		a, _ := time.Parse("2006-01-02", st.LatestDate)
		o, _ := time.Parse("2006-01-02", ourLatest)
		if !a.IsZero() && !o.IsZero() {
			st.LagDays = int(o.Sub(a).Hours() / 24)
		}
	}
	st.Synced = st.LagDays <= 0 && st.LatestDate != ""
	return st
}

// distributionSettings 保存平台配置（settings 表）。
func (h *Handler) distributionSettings(w http.ResponseWriter, r *http.Request) {
	for key := range map[string]struct{}{
		"apple_show_id": {}, "apple_country": {}, "xiaoyuzhou_url": {},
	} {
		if err := h.store.SetSetting(key, strings.TrimSpace(r.FormValue(key))); err != nil {
			h.log.Error("set setting", "key", key, "err", err)
			writeErr(w, http.StatusInternalServerError, "save failed")
			return
		}
	}
	h.redirect(w, r, "/admin/distribution")
}

func urlQueryEscape(s string) string { return strings.ReplaceAll(s, " ", "%20") }

func hostOf(raw string) string {
	u, err := parseURL(raw)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

func parseURL(raw string) (*url.URL, error) { return url.Parse(raw) }
