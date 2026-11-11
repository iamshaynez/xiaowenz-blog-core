package mailer

import (
	"net/url"
	"strings"
	"unicode"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// emailStyles 是注入正文各元素的邮件安全 inline style，取自博客玄墨
// 主题（site/themes/xuanmo app.css）的排版基因：纸底 #F1F1EF、玄黑
// #141210、冷漆红 #CE1432 只点睛、发丝线 #D8D6D1、宋体衬线正文 1.85
// 行高。邮件客户端（尤其 Gmail）会 strip <style> 块，正文样式必须逐
// 元素内联；已有 style 属性的元素不覆盖。
var emailStyles = map[string]string{
	"h1": "margin:2.2em 0 .6em;line-height:1.35;font-size:1.5em;font-weight:700;letter-spacing:-0.01em;",
	"h2": "margin:2.2em 0 .6em;line-height:1.35;font-size:1.3em;font-weight:700;letter-spacing:-0.01em;",
	"h3": "margin:1.8em 0 .5em;line-height:1.4;font-size:1.15em;font-weight:700;",
	"h4": "margin:1.8em 0 .5em;line-height:1.4;font-size:1.05em;font-weight:700;",
	"h5": "margin:1.6em 0 .4em;line-height:1.4;font-weight:700;",
	"h6": "margin:1.6em 0 .4em;line-height:1.4;font-weight:700;",
	"p":  "margin:0 0 1.35em;line-height:1.85;letter-spacing:0.01em;",
	"ul": "margin:0 0 1.35em;padding-left:1.4em;",
	"ol": "margin:0 0 1.35em;padding-left:1.4em;",
	"li": "margin:.4em 0;line-height:1.8;",
	"blockquote": "margin:2em 0;padding:0 0 0 1.25em;border-left:2px solid #CE1432;" +
		"color:#5A544C;font-size:1.1em;line-height:1.65;",
	"pre": "margin:1.75em 0;padding:1.25em 1.4em;background:#141210;color:#DCD7CE;" +
		"border-radius:4px;font-size:.875em;line-height:1.65;overflow-x:auto;",
	"code": "font-family:'JetBrains Mono','SF Mono',Menlo,Consolas,monospace;" +
		"font-size:.88em;background:rgba(20,18,16,0.05);padding:.15em .4em;border-radius:2px;",
	"img":    "display:block;max-width:100%;height:auto;border-radius:4px;margin:2em auto;",
	"a":      "color:#141210;text-decoration:underline;text-decoration-color:#CE1432;text-decoration-thickness:1.5px;",
	"hr":     "border:0;height:1px;background:#D8D6D1;margin:2.5em 0;",
	"table":  "border-collapse:collapse;margin:0 0 1.75em;width:100%;font-size:.875em;",
	"th":     "font-weight:500;letter-spacing:0.08em;text-align:left;border-bottom:1px solid #141210;padding:.7em .9em;",
	"td":     "color:#5A544C;border-bottom:1px solid #D8D6D1;padding:.7em .9em;line-height:1.6;",
	"figure": "margin:2em 0;",
	"figcaption": "font-family:'Noto Sans SC','PingFang SC',sans-serif;font-size:12px;" +
		"color:#9A948C;letter-spacing:0.08em;text-align:center;margin-top:.75em;line-height:1.5;",
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
	dropCapped := false
	for _, n := range nodes {
		if emailizeDrop(n) {
			continue
		}
		emailizeNode(n, base)
		if !dropCapped {
			dropCapped = dropCap(n)
		}
		_ = html.Render(&b, n)
	}
	return b.String()
}

// dropCap 对杂志式首字下沉：把顶层第一个 <p> 的首个文字字符包进
// float:left 的大号衬线 span（玄墨 .prose > p:first-of-type::first-letter
// 的邮件版）。首子节点不是文本或段落过短时保守跳过；不支持 float 的
// 客户端（老 Outlook）自动降级为普通内联字符。
func dropCap(p *html.Node) bool {
	if p.Type != html.ElementNode || p.Data != "p" {
		return false
	}
	first := p.FirstChild
	if first == nil || first.Type != html.TextNode {
		return false
	}
	runes := []rune(first.Data)
	if len(runes) < 2 || unicode.IsSpace(runes[0]) {
		return false
	}
	span := &html.Node{
		Type: html.ElementNode, Data: "span", DataAtom: atom.Span,
		Attr: []html.Attribute{{Key: "style", Val: "float:left;font-size:3.4em;" +
			"font-weight:700;line-height:0.85;margin:0.08em 0.12em 0 0;color:#141210;"}},
	}
	span.AppendChild(&html.Node{Type: html.TextNode, Data: string(runes[0])})
	p.InsertBefore(span, first)
	first.Data = string(runes[1:])
	return true
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
