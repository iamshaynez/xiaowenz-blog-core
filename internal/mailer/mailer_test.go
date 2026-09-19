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
