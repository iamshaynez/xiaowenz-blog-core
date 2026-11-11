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

	xhtml "golang.org/x/net/html"

	"github.com/iamshaynez/xiaowenz-blog-core/internal/mailer"
	"github.com/yuin/goldmark"
)

// newsletterImport — 从博客文章 URL 一键生成 Newsletter 草稿（SPEC §4）：
// 抓取页面取标题与正文容器全文，正文经邮件化清洗（URL 绝对化、去 schema
// 噪音、注入 inline style）后整篇进草稿，标题进主题，post_url 记录来源。
// 页面没有正文容器时回退摘要模式（og:description + 阅读全文链接）。
// 绝不直接发送——发送永远由人在草稿页点。
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

	var markdown, bodyHTML string
	if article := extractArticleBody(page); strings.TrimSpace(article) != "" {
		// 全文模式：正文容器内容邮件化后整篇进草稿。
		markdown = "全文导入：" + raw
		bodyHTML = mailer.EmailizeHTML(article, raw)
	} else {
		// 摘要模式回退：导语 + 阅读全文链接。
		markdown = buildImportMarkdown(title, desc, raw, h.cfg.SMTPFrom)
		var buf bytes.Buffer
		if err := goldmark.Convert([]byte(markdown), &buf); err != nil {
			writeErr(w, http.StatusInternalServerError, "markdown render failed")
			return
		}
		bodyHTML = buf.String()
	}
	id, err := h.store.CreateNewsletter(title, markdown, bodyHTML, raw)
	if err != nil {
		h.log.Error("create newsletter", "err", err)
		writeErr(w, http.StatusInternalServerError, "create failed")
		return
	}
	h.log.Info("newsletter draft imported", "id", id, "url", raw)
	h.redirect(w, r, "/admin/newsletters/"+strconv.FormatInt(id, 10))
}

// extractArticleBody 返回文章正文容器的 inner HTML：优先玄墨主题的
// div.article__body，兼容任意带 e-content class 或 itemprop="articleBody"
// 的元素（microformats 锚点）。找不到或解析失败返回 ""。
func extractArticleBody(page []byte) string {
	doc, err := xhtml.Parse(bytes.NewReader(page))
	if err != nil {
		return ""
	}
	var find func(*xhtml.Node) *xhtml.Node
	find = func(n *xhtml.Node) *xhtml.Node {
		if n.Type == xhtml.ElementNode && n.Data == "div" && isArticleBody(n) {
			return n
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if found := find(c); found != nil {
				return found
			}
		}
		return nil
	}
	node := find(doc)
	if node == nil {
		return ""
	}
	var b strings.Builder
	for c := node.FirstChild; c != nil; c = c.NextSibling {
		_ = xhtml.Render(&b, c)
	}
	return b.String()
}

// isArticleBody 识别正文容器：class 含 article__body 或 e-content，
// 或带 itemprop="articleBody"。
func isArticleBody(n *xhtml.Node) bool {
	for _, a := range n.Attr {
		switch a.Key {
		case "class":
			for _, cls := range strings.Fields(a.Val) {
				if cls == "article__body" || cls == "e-content" {
					return true
				}
			}
		case "itemprop":
			if a.Val == "articleBody" {
				return true
			}
		}
	}
	return false
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
