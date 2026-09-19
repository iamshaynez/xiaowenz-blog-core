package mailer

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
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

// NewsletterShell carries the newsletter body plus footer fields.
type NewsletterShell struct {
	BodyHTML       template.HTML
	UnsubURL       string
	MailingAddress string
}

// RenderNewsletter wraps rendered markdown HTML in the brand shell with a
// personal unsubscribe link and the physical mailing address.
func RenderNewsletter(s NewsletterShell) (html, text string, err error) {
	html, err = render("newsletter.html", s)
	if err != nil {
		return "", "", err
	}
	text = "「三杯过后」通讯\n\n（本期为 HTML 内容，请在支持 HTML 的客户端中查看。）\n\n" +
		"退订本通讯：" + s.UnsubURL + "\n" + s.MailingAddress
	return html, text, nil
}
