package admin

import (
	"bytes"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/yuin/goldmark"
)

// newsletterImport — 从博客文章 URL 一键生成 Newsletter 草稿（SPEC §4）：
// 抓取页面取标题与 og:description，标题进主题、导语+原文链接进正文，
// 落库为 draft。绝不直接发送——发送永远由人在草稿页点。
//
// 安全面：URL 的 host 必须落在 CORE_SITE_URL 派生的域名后缀内
// （如 site URL 是 blog.xiaowenz.com → 允许 xiaowenz.com 与 *.xiaowenz.com），
// 拒绝任意站外地址（SSRF 防线）。

var (
	reTitle    = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	reOGTitle  = regexp.MustCompile(`(?is)<meta[^>]+property=["']og:title["'][^>]+content=["']([^"']*)["']`)
	reOGDesc   = regexp.MustCompile(`(?is)<meta[^>]+property=["']og:description["'][^>]+content=["']([^"']*)["']`)
	reMetaDesc = regexp.MustCompile(`(?is)<meta[^>]+name=["']description["'][^>]+content=["']([^"']*)["']`)
)

// importHostAllowed reports whether host falls inside the site's domain suffix.
func (h *Handler) importHostAllowed(host string) bool {
	if h.importHostSuffix == "" {
		return false
	}
	return host == h.importHostSuffix || strings.HasSuffix(host, "."+h.importHostSuffix)
}

// siteHostSuffix derives the registrable-ish suffix (last two labels) from
// CORE_SITE_URL, e.g. "https://blog.xiaowenz.com" → "xiaowenz.com".
func siteHostSuffix(siteURL string) string {
	u, err := url.Parse(siteURL)
	if err != nil || u.Hostname() == "" {
		return ""
	}
	labels := strings.Split(u.Hostname(), ".")
	if len(labels) < 2 {
		return u.Hostname()
	}
	return strings.Join(labels[len(labels)-2:], ".")
}

func (h *Handler) newsletterImport(w http.ResponseWriter, r *http.Request) {
	raw := strings.TrimSpace(r.FormValue("url"))
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || !h.importHostAllowed(u.Hostname()) {
		writeErr(w, http.StatusBadRequest, "url must point at the blog site")
		return
	}
	resp, err := h.importClient.Get(raw)
	if err != nil {
		h.log.Warn("newsletter import fetch", "err", err)
		writeErr(w, http.StatusBadGateway, "fetch failed")
		return
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		writeErr(w, http.StatusBadGateway, "post not reachable: "+strconv.Itoa(resp.StatusCode))
		return
	}
	page, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		writeErr(w, http.StatusBadGateway, "fetch failed")
		return
	}

	title := firstMatch(reOGTitle, page)
	if title == "" {
		title = firstMatch(reTitle, page)
	}
	desc := firstMatch(reOGDesc, page)
	if desc == "" {
		desc = firstMatch(reMetaDesc, page)
	}
	if title == "" {
		writeErr(w, http.StatusBadGateway, "post title not found")
		return
	}

	md := buildImportMarkdown(title, desc, raw, h.cfg.SMTPFrom)
	var buf bytes.Buffer
	if err := goldmark.Convert([]byte(md), &buf); err != nil {
		writeErr(w, http.StatusInternalServerError, "markdown render failed")
		return
	}
	id, err := h.store.CreateNewsletter(title, md, buf.String())
	if err != nil {
		h.log.Error("create newsletter", "err", err)
		writeErr(w, http.StatusInternalServerError, "create failed")
		return
	}
	h.log.Info("newsletter draft imported", "id", id, "url", raw)
	h.redirect(w, r, "/admin/newsletters/"+strconv.FormatInt(id, 10))
}

// firstMatch returns the unescaped, whitespace-stripped first capture of re in
// page, or "".
func firstMatch(re *regexp.Regexp, page []byte) string {
	m := re.FindSubmatch(page)
	if m == nil {
		return ""
	}
	s := strings.TrimSpace(html.UnescapeString(string(m[1])))
	// title 里不应有内嵌标签
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' {
			return ' '
		}
		return r
	}, s)
	return strings.TrimSpace(s)
}

// buildImportMarkdown assembles the draft body from a published post.
func buildImportMarkdown(title, desc, postURL, smtpFrom string) string {
	var b strings.Builder
	b.WriteString("**" + title + "**\n\n")
	if desc != "" {
		b.WriteString(desc + "\n\n")
	}
	b.WriteString("→ [阅读全文](" + postURL + ")")
	if name := displayName(smtpFrom); name != "" {
		b.WriteString("\n\n—— " + name)
	}
	return b.String()
}

// displayName extracts "三杯过后" out of `三杯过后 <letter@example.com>`.
func displayName(from string) string {
	if i := strings.LastIndex(from, "<"); i > 0 {
		return strings.TrimSpace(from[:i])
	}
	return ""
}

// originOf returns scheme://host of rawurl, or "" when unparseable.
func originOf(rawurl string) string {
	u, err := url.Parse(rawurl)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}
