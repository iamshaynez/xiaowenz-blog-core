package mailer

import (
	"net/url"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// emailStyles 是注入正文各元素的邮件安全 inline style。邮件客户端
// （尤其 Gmail）会 strip <style> 块，正文样式必须逐元素内联；已有
// style 属性的元素不覆盖。
var emailStyles = map[string]string{
	"h1": "margin:1.4em 0 .7em;line-height:1.35;font-size:1.5em;font-weight:700;",
	"h2": "margin:1.4em 0 .7em;line-height:1.35;font-size:1.3em;font-weight:700;",
	"h3": "margin:1.2em 0 .6em;line-height:1.4;font-size:1.15em;font-weight:700;",
	"h4": "margin:1.2em 0 .6em;line-height:1.4;font-size:1.05em;font-weight:700;",
	"h5": "margin:1.1em 0 .5em;line-height:1.4;font-weight:700;",
	"h6": "margin:1.1em 0 .5em;line-height:1.4;font-weight:700;",
	"p":  "margin:0 0 1em;line-height:1.75;",
	"ul": "margin:0 0 1em;padding-left:1.4em;",
	"ol": "margin:0 0 1em;padding-left:1.4em;",
	"li": "margin:.25em 0;line-height:1.7;",
	"blockquote": "margin:0 0 1em;padding:.6em 1em;border-left:3px solid #222;" +
		"color:#555;background:#f7f7f7;line-height:1.7;",
	"pre": "margin:0 0 1em;padding:.8em;background:#f5f5f5;border-radius:6px;" +
		"overflow-x:auto;font-size:.9em;line-height:1.55;",
	"code": "font-family:ui-monospace,Menlo,Consolas,monospace;font-size:.92em;" +
		"background:#f5f5f5;padding:.15em .35em;border-radius:3px;",
	"img":        "max-width:100%;height:auto;border-radius:6px;margin:.5em 0;",
	"a":          "color:#222;text-decoration:underline;",
	"hr":         "border:none;border-top:1px solid #ddd;margin:1.5em 0;",
	"table":      "border-collapse:collapse;margin:0 0 1em;width:100%;font-size:.95em;",
	"th":         "border:1px solid #ddd;padding:.4em .6em;text-align:left;background:#f7f7f7;",
	"td":         "border:1px solid #ddd;padding:.4em .6em;line-height:1.6;",
	"figure":     "margin:0 0 1em;",
	"figcaption": "color:#888;font-size:.85em;margin-top:.4em;line-height:1.5;",
}

// EmailizeHTML 把文章正文 HTML 变成邮件就绪 HTML：清除 schema.org
// itemprop/itemscope 噪音与 script/style/meta/link，相对 URL 以 baseURL
// （文章地址）绝对化（baseURL 为空时跳过），并注入 inline style。
// 入参是可信的管理员内容；解析失败时原样返回。
func EmailizeHTML(body, baseURL string) string {
	var base *url.URL
	if baseURL != "" {
		if u, err := url.Parse(baseURL); err == nil && u.IsAbs() {
			base = u
		}
	}
	nodes, err := html.ParseFragment(strings.NewReader(body), &html.Node{
		Type: html.ElementNode, Data: "body", DataAtom: atom.Body,
	})
	if err != nil || len(nodes) == 0 {
		return body
	}
	var b strings.Builder
	for _, n := range nodes {
		if emailizeDrop(n) {
			continue
		}
		emailizeNode(n, base)
		_ = html.Render(&b, n)
	}
	return b.String()
}

// emailizeNode 就地清洗一棵片段子树。
func emailizeNode(n *html.Node, base *url.URL) {
	if n.Type != html.ElementNode {
		return
	}
	for c := n.FirstChild; c != nil; {
		next := c.NextSibling
		if emailizeDrop(c) {
			n.RemoveChild(c)
		} else {
			emailizeNode(c, base)
		}
		c = next
	}
	if base != nil {
		emailizeAbsolutize(n, base)
	}
	if st, ok := emailStyles[n.Data]; ok && attrVal(n, "style") == "" {
		n.Attr = append(n.Attr, html.Attribute{Key: "style", Val: st})
	}
}

// emailizeDrop 报告一个节点是否该整棵丢弃：script/style/meta/link
// 或带 itemprop/itemscope 属性的 schema.org 结构噪音。
func emailizeDrop(n *html.Node) bool {
	if n.Type != html.ElementNode {
		return false
	}
	switch n.Data {
	case "script", "style", "meta", "link":
		return true
	}
	for _, a := range n.Attr {
		if a.Key == "itemprop" || a.Key == "itemscope" {
			return true
		}
	}
	return false
}

// emailizeAbsolutize 把 a[href] / img[src] / source[src] 的相对引用解析成
// 基于 baseURL 的绝对地址；锚点（#…）与绝对 URL 不动。
func emailizeAbsolutize(n *html.Node, base *url.URL) {
	key := ""
	switch n.Data {
	case "a":
		key = "href"
	case "img", "source":
		key = "src"
	default:
		return
	}
	for i, a := range n.Attr {
		if a.Key != key || a.Val == "" || strings.HasPrefix(a.Val, "#") {
			continue
		}
		ref, err := url.Parse(a.Val)
		if err != nil || ref.IsAbs() {
			continue
		}
		n.Attr[i].Val = base.ResolveReference(ref).String()
	}
}

func attrVal(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

// htmlToText 提取 HTML 的纯文本（text 备选用）：块级元素前后断行，
// <br> 转行，script/style 跳过，连续空行折叠为一段。
func htmlToText(bodyHTML string) string {
	doc, err := html.Parse(strings.NewReader(bodyHTML))
	if err != nil {
		return ""
	}
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		switch n.Type {
		case html.ElementNode:
			switch n.Data {
			case "script", "style":
				return
			case "br":
				b.WriteByte('\n')
				return
			case "p", "div", "h1", "h2", "h3", "h4", "h5", "h6",
				"li", "tr", "blockquote", "pre", "figure", "table":
				b.WriteByte('\n')
				for c := n.FirstChild; c != nil; c = c.NextSibling {
					walk(c)
				}
				b.WriteByte('\n')
				return
			}
		case html.TextNode:
			b.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	lines := strings.Split(b.String(), "\n")
	var out []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" || (len(out) > 0 && out[len(out)-1] != "") {
			out = append(out, line)
		}
	}
	return strings.Trim(strings.Join(out, "\n"), "\n")
}
