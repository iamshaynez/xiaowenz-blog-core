package mailer

import (
	"strings"
	"testing"
)

func TestFakeRecordsSends(t *testing.T) {
	f := &Fake{}
	err := f.Send("a@x.com", "subject", "<p>hi</p>", "hi", map[string]string{"List-Unsubscribe": "<https://u>"})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(f.Sent) != 1 {
		t.Fatalf("sent = %d, want 1", len(f.Sent))
	}
	m := f.Sent[0]
	if m.To != "a@x.com" || m.Subject != "subject" || m.Text != "hi" {
		t.Errorf("message = %+v", m)
	}
	if m.Headers["List-Unsubscribe"] != "<https://u>" {
		t.Errorf("headers = %v", m.Headers)
	}
}

func TestRenderVerifyEmail(t *testing.T) {
	html, text, err := RenderVerify("https://core.example.com/api/verify?token=abc123")
	if err != nil {
		t.Fatalf("RenderVerify: %v", err)
	}
	for _, body := range []string{html, text} {
		if !strings.Contains(body, "https://core.example.com/api/verify?token=abc123") {
			t.Errorf("verify link missing from body: %q", body)
		}
	}
	if !strings.Contains(html, "三杯过后") {
		t.Error("brand name missing from HTML")
	}
}

func TestRenderWelcomeEmail(t *testing.T) {
	html, text, err := RenderWelcome("https://core.example.com/api/unsub?token=xyz")
	if err != nil {
		t.Fatalf("RenderWelcome: %v", err)
	}
	if !strings.Contains(html, "https://core.example.com/api/unsub?token=xyz") {
		t.Error("unsub link missing from welcome HTML")
	}
	if !strings.Contains(text, "https://core.example.com/api/unsub?token=xyz") {
		t.Error("unsub link missing from welcome text")
	}
}

func TestRenderCommentNotify(t *testing.T) {
	html, text, err := RenderCommentNotify(CommentNotice{
		PostSlug:   "ep-01",
		AuthorName: "小明",
		Content:    "好评",
		ApproveURL: "https://core.example.com/api/comments/moderate?token=sigA",
		RejectURL:  "https://core.example.com/api/comments/moderate?token=sigR",
	})
	if err != nil {
		t.Fatalf("RenderCommentNotify: %v", err)
	}
	if !strings.Contains(html, "token=sigA") || !strings.Contains(html, "token=sigR") {
		t.Error("one-click links missing from notify HTML")
	}
	if !strings.Contains(text, "token=sigA") || !strings.Contains(text, "好评") {
		t.Error("one-click link or content missing from notify text")
	}
	if !strings.Contains(html, "小明") || !strings.Contains(html, "ep-01") {
		t.Error("author/post missing from notify HTML")
	}
}

func TestRenderNewsletterShell(t *testing.T) {
	html, text, err := RenderNewsletter(NewsletterShell{
		BodyHTML:       "<h1>标题</h1><p>正文</p>",
		UnsubURL:       "https://core.example.com/api/unsub?token=personal",
		MailingAddress: "某市某路 1 号",
	})
	if err != nil {
		t.Fatalf("RenderNewsletter: %v", err)
	}
	if !strings.Contains(html, "<h1>标题</h1>") {
		t.Error("body HTML not embedded")
	}
	if !strings.Contains(html, "三杯过后") {
		t.Error("brand missing")
	}
	if !strings.Contains(html, "token=personal") {
		t.Error("personal unsub link missing")
	}
	if !strings.Contains(html, "某市某路 1 号") {
		t.Error("mailing address missing")
	}
	if !strings.Contains(text, "token=personal") {
		t.Error("unsub link missing from text alternative")
	}
}

func TestRenderNewsletterFullLayout(t *testing.T) {
	html, text, err := RenderNewsletter(NewsletterShell{
		Title:          "文章标题",
		PostURL:        "https://xiaowenz.com/blog/a.html",
		BodyHTML:       "<h2>小节</h2><p>正文段落</p>",
		UnsubURL:       "https://core.example.com/api/unsub?token=personal",
		MailingAddress: "某市某路 1 号",
	})
	if err != nil {
		t.Fatalf("RenderNewsletter: %v", err)
	}
	if !strings.Contains(html, "三杯过后") || !strings.Contains(html, "专栏博客文章订阅") {
		t.Error("品牌页眉「三杯过后 · 专栏博客文章订阅」missing")
	}
	if !strings.Contains(html, "文章标题") {
		t.Error("文章大标题 missing")
	}
	if !strings.Contains(html, "<p>正文段落</p>") {
		t.Error("全文正文 missing")
	}
	if !strings.Contains(html, `href="https://xiaowenz.com/blog/a.html"`) || !strings.Contains(html, "阅读原文") {
		t.Error("阅读原文链接 missing")
	}
	if !strings.Contains(html, "token=personal") || !strings.Contains(html, "退订") {
		t.Error("退订链接 missing")
	}
	if !strings.Contains(html, "某市某路 1 号") {
		t.Error("mailing address missing")
	}
	// text 备选不再是占位文案，而是真实正文纯文本。
	for _, want := range []string{"文章标题", "小节", "正文段落", "阅读原文：https://xiaowenz.com/blog/a.html", "token=personal"} {
		if !strings.Contains(text, want) {
			t.Errorf("text 备选缺 %q: %q", want, text)
		}
	}
}

func TestRenderNewsletterNoPostURLHidesButton(t *testing.T) {
	html, _, err := RenderNewsletter(NewsletterShell{
		Title:    "手动撰写",
		BodyHTML: "<p>正文</p>",
		UnsubURL: "https://core.example.com/api/unsub?token=t",
	})
	if err != nil {
		t.Fatalf("RenderNewsletter: %v", err)
	}
	if strings.Contains(html, "阅读原文") {
		t.Error("无 PostURL 时不应渲染「阅读原文」按钮")
	}
}

func TestEmailizeHTML(t *testing.T) {
	in := `<h2>小节</h2>` +
		`<p>见 <a href="/about/">关于</a>、<img src="img/a.png" alt="a"> 与 <a href="https://ext.com/x">外链</a></p>` +
		`<div itemprop="image" itemscope><meta itemprop="url" content="x"></div>` +
		`<meta itemprop="dateModified" content="2026">` +
		`<script>alert(1)</script>`
	got := EmailizeHTML(in, "https://xiaowenz.com/blog/post.html")
	if !strings.Contains(got, `href="https://xiaowenz.com/about/"`) {
		t.Errorf("相对链接未绝对化: %s", got)
	}
	if !strings.Contains(got, `src="https://xiaowenz.com/blog/img/a.png"`) {
		t.Errorf("相对图片未按文章目录绝对化: %s", got)
	}
	if !strings.Contains(got, `href="https://ext.com/x"`) {
		t.Errorf("绝对链接不应被改动: %s", got)
	}
	for _, noise := range []string{"itemprop", "itemscope", "<script", "dateModified"} {
		if strings.Contains(got, noise) {
			t.Errorf("schema/script 噪音 %q 未清除: %s", noise, got)
		}
	}
	if !strings.Contains(got, "<p style=") || !strings.Contains(got, "<h2 style=") {
		t.Errorf("inline style 未注入: %s", got)
	}
	if strings.Contains(got, "<a href=\"/about/\"") {
		t.Errorf("原相对 href 仍残留: %s", got)
	}
}

func TestEmailizeHTMLEmptyBaseSkipsAbsolutize(t *testing.T) {
	got := EmailizeHTML(`<a href="/x">y</a><p>z</p>`, "")
	if !strings.Contains(got, `href="/x"`) {
		t.Errorf("baseURL 为空时不应改动 URL: %s", got)
	}
	if !strings.Contains(got, "<p style=") {
		t.Errorf("style 注入不应依赖 baseURL: %s", got)
	}
}

func TestEmailizeHTMLKeepsExistingStyle(t *testing.T) {
	got := EmailizeHTML(`<p style="color:red">x</p>`, "")
	if !strings.Contains(got, `style="color:red"`) || strings.Count(got, "style=") != 1 {
		t.Errorf("已有 style 不应被覆盖或重复: %s", got)
	}
}

func TestHTMLToText(t *testing.T) {
	got := htmlToText("<h2>标题</h2><p>第一段<br>第二行</p><ul><li>项</li></ul>")
	for _, want := range []string{"标题", "第一段", "第二行", "项"} {
		if !strings.Contains(got, want) {
			t.Errorf("缺 %q: %q", want, got)
		}
	}
	if !strings.Contains(got, "第一段\n第二行") {
		t.Errorf("br 应转行: %q", got)
	}
}

func TestHeaderInjectionFiltered(t *testing.T) {
	if got := FilterHeader("ok\r\nBcc: evil@x.com"); got != "okBcc: evil@x.com" {
		t.Errorf("CRLF not stripped: %q", got)
	}
	if got := FilterHeader("clean"); got != "clean" {
		t.Errorf("clean value mangled: %q", got)
	}
}

func TestSMTPAgainstFakeServer(t *testing.T) {
	addr, received := startFakeSMTP(t)
	host, port := splitHostPort(t, addr)

	m := NewSMTP(host, port, "", "", "三杯过后 <letter@example.com>")
	err := m.Send("rcpt@example.com", "你好", "<p>body</p>", "body",
		map[string]string{
			"List-Unsubscribe": "<https://core.example.com/api/unsub?token=t>",
			"X-Evil":           "v\r\nBcc: attacker@x.com",
		})
	if err != nil {
		t.Fatalf("SMTP Send: %v", err)
	}
	msg := <-received
	if !strings.Contains(msg, "From:") || !strings.Contains(msg, "letter@example.com") {
		t.Error("From header missing")
	}
	if !strings.Contains(msg, "To: rcpt@example.com") {
		t.Error("To header missing")
	}
	if !strings.Contains(msg, "List-Unsubscribe: <https://core.example.com/api/unsub?token=t>") {
		t.Error("List-Unsubscribe header missing")
	}
	if strings.Contains(msg, "\nBcc: attacker@x.com") || strings.HasPrefix(msg, "Bcc: attacker@x.com") {
		t.Error("CRLF injection created a new header line")
	}
	// MIME multipart alternative must carry both html and text parts.
	if !strings.Contains(msg, "text/html") || !strings.Contains(msg, "text/plain") {
		t.Error("multipart alternative parts missing")
	}
	// UTF-8 subject must be encoded per RFC 2047.
	if !strings.Contains(msg, "Subject: =?UTF-8?") {
		t.Errorf("subject not encoded: %s", msg)
	}
}

// 杂志化排版（玄墨基因）：纸底、红 eyebrow、宋体衬线正文、黑按钮。
func TestRenderNewsletterMagazineStyle(t *testing.T) {
	html, _, err := RenderNewsletter(NewsletterShell{
		Title:    "文章标题",
		PostURL:  "https://x.com/p.html",
		BodyHTML: "<p>正文</p>",
		UnsubURL: "https://u.example/unsub",
		Bio:      "我是晓文。\n写代码也写字。",
	})
	if err != nil {
		t.Fatalf("RenderNewsletter: %v", err)
	}
	for _, want := range []string{
		"#F1F1EF",                       // 纸底
		"#CE1432",                       // 冷漆红点睛（eyebrow 线/字）
		"专栏博客文章订阅",                      // eyebrow 文案
		"'Noto Serif SC'",               // 宋体衬线栈
		"我是谁 WHO AM I",                  // bio 区块小标题
		"我是晓文。<br>",                     // bio 多行转 <br>
		"text-decoration-color:#CE1432", // 链接红下划线
		"background:#141210",            // 玄黑「阅读原文」按钮
	} {
		if !strings.Contains(html, want) {
			t.Errorf("杂志化邮件缺 %q", want)
		}
	}
}

// bio 为空时整个「我是谁」区块不渲染。
func TestRenderNewsletterBioEmptyHidesBlock(t *testing.T) {
	html, text, err := RenderNewsletter(NewsletterShell{
		Title:    "t",
		BodyHTML: "<p>x</p>",
		UnsubURL: "https://u.example/unsub",
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(html, "WHO AM I") || strings.Contains(html, "我是谁") {
		t.Error("空 bio 不应渲染「我是谁」区块")
	}
	if strings.Contains(text, "我是谁") {
		t.Error("text 版同理")
	}
}

// BioToHTML：HTML 转义 + 换行转 <br>（bio 是管理员输入的纯文本）。
func TestBioToHTML(t *testing.T) {
	got := BioToHTML("<b>加粗</b>\n第二行")
	if strings.Contains(string(got), "<b>") {
		t.Errorf("未转义 HTML: %q", got)
	}
	if !strings.Contains(string(got), "&lt;b&gt;加粗&lt;/b&gt;<br>第二行") {
		t.Errorf("转义/换行结果不对: %q", got)
	}
	if BioToHTML("  ") != "" {
		t.Error("空白输入应得空串")
	}
}

// text 备选含 bio 原文（保留换行）。
func TestRenderNewsletterTextCarriesBio(t *testing.T) {
	_, text, err := RenderNewsletter(NewsletterShell{
		Title:    "t",
		BodyHTML: "<p>正文</p>",
		UnsubURL: "https://u.example/unsub",
		Bio:      "第一行介绍\n第二行介绍",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "我是谁 WHO AM I") || !strings.Contains(text, "第一行介绍\n第二行介绍") {
		t.Errorf("text 版缺 bio: %q", text)
	}
}

// 首字下沉：顶层第一个 p 的首个字符包 float span；首子节点是元素时跳过。
func TestEmailizeHTMLDropCap(t *testing.T) {
	got := EmailizeHTML("<p>你好，世界。</p><p>第二段。</p>", "")
	if !strings.Contains(got, `<span style="float:left;`) {
		t.Errorf("首字未下沉: %s", got)
	}
	if !strings.Contains(got, `>你</span>`) {
		t.Errorf("下沉的应是首字「你」: %s", got)
	}
	if got2 := EmailizeHTML("<p><strong>整段加粗</strong></p>", ""); strings.Contains(got2, "float:left") {
		t.Errorf("首子节点是元素时应跳过: %s", got2)
	}
	if got3 := EmailizeHTML("<h2>标题先行</h2><p>再有正文</p>", ""); !strings.Contains(got3, "float:left") {
		t.Errorf("首个顶层 p（即使前面有标题）应下沉: %s", got3)
	}
}
