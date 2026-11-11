package mailer

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"strings"
)

//go:embed templates/*.html
var templatesFS embed.FS

var tpl = template.Must(template.ParseFS(templatesFS, "templates/*.html"))

func render(name string, data any) (string, error) {
	var buf bytes.Buffer
	if err := tpl.ExecuteTemplate(&buf, name, data); err != nil {
		return "", fmt.Errorf("render %s: %w", name, err)
	}
	return buf.String(), nil
}

// RenderVerify builds the double opt-in confirmation email (HTML + text).
func RenderVerify(verifyURL string) (html, text string, err error) {
	html, err = render("verify.html", map[string]string{"VerifyURL": verifyURL})
	if err != nil {
		return "", "", err
	}
	text = "欢迎订阅「三杯过后」！请点击以下链接确认订阅（24 小时内有效）：\n\n" + verifyURL +
		"\n\n如果这不是你的操作，请忽略本邮件。"
	return html, text, nil
}

// RenderWelcome builds the post-confirmation welcome email (HTML + text).
func RenderWelcome(unsubURL string) (html, text string, err error) {
	html, err = render("welcome.html", map[string]string{"UnsubURL": unsubURL})
	if err != nil {
		return "", "", err
	}
	text = "订阅已确认，欢迎加入「三杯过后」！\n\n每期通讯都会发送到这个邮箱。" +
		"如需退订，随时点击：\n" + unsubURL
	return html, text, nil
}

// CommentNotice carries the fields of the admin comment notification.
type CommentNotice struct {
	PostSlug   string
	AuthorName string
	Content    string
	ApproveURL string
	RejectURL  string
}

// RenderCommentNotify builds the admin notification with one-click links.
func RenderCommentNotify(n CommentNotice) (html, text string, err error) {
	html, err = render("comment_notify.html", n)
	if err != nil {
		return "", "", err
	}
	text = fmt.Sprintf("新评论待审核\n\n文章：%s\n作者：%s\n内容：%s\n\n通过：%s\n拒绝：%s\n",
		n.PostSlug, n.AuthorName, n.Content, n.ApproveURL, n.RejectURL)
	return html, text, nil
}

// NewsletterShell carries the newsletter article plus footer fields.
type NewsletterShell struct {
	Title          string        // 文章大标题（= 邮件主题）
	PostURL        string        // 来源文章地址（空 = 手动撰写，隐藏「阅读原文」）
	BodyHTML       template.HTML // 邮件化后的全文正文（EmailizeHTML 产物）
	UnsubURL       string        // 收件人专属退订链接
	MailingAddress string
	Bio            string // 页脚「我是谁 WHO AM I」多行介绍（空 = 隐藏区块）
}

// BioToHTML 把管理员输入的多行纯文本 bio 转为邮件 HTML：HTML 转义 +
// 换行转 <br>；空白输入得空串。
func BioToHTML(text string) template.HTML {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	return template.HTML(strings.ReplaceAll(template.HTMLEscapeString(text), "\n", "<br>"))
}

// RenderNewsletter wraps the full-article body in the brand shell（玄墨杂志
// 风格）：红 eyebrow 品牌行、衬线大标题、全文、「阅读原文」玄黑按钮与
// 页脚（bio 区块 + 退订）。text 备选携带真实正文纯文本。
func RenderNewsletter(s NewsletterShell) (html, text string, err error) {
	data := struct {
		NewsletterShell
		BioHTML template.HTML
	}{s, BioToHTML(s.Bio)}
	html, err = render("newsletter.html", data)
	if err != nil {
		return "", "", err
	}
	var b strings.Builder
	if s.Title != "" {
		b.WriteString(s.Title + "\n\n")
	}
	if body := htmlToText(string(s.BodyHTML)); body != "" {
		b.WriteString(body + "\n\n")
	}
	if s.PostURL != "" {
		b.WriteString("阅读原文：" + s.PostURL + "\n\n")
	}
	b.WriteString("——\n「三杯过后」专栏博客文章订阅\n")
	if s.Bio != "" {
		b.WriteString("\n我是谁 WHO AM I\n" + s.Bio + "\n\n")
	}
	b.WriteString("退订本通讯：" + s.UnsubURL + "\n" + s.MailingAddress + "\n")
	return html, b.String(), nil
}
