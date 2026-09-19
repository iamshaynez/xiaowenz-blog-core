package mailer

import (
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"mime"
	"net/smtp"
	"strings"
)

// SMTPMailer delivers over SMTP: port 465 = implicit TLS, otherwise plain
// with opportunistic STARTTLS (587). Implements Mailer.
type SMTPMailer struct {
	host string
	port int
	user string
	pass string
	from string
}

// NewSMTP builds an SMTPMailer.
func NewSMTP(host string, port int, user, pass, from string) *SMTPMailer {
	return &SMTPMailer{host: host, port: port, user: user, pass: pass, from: from}
}

// Send builds a multipart/alternative MIME message and delivers it.
func (m *SMTPMailer) Send(to, subject, html, text string, headers map[string]string) error {
	msg := buildMessage(m.from, to, subject, html, text, headers)
	addr := fmt.Sprintf("%s:%d", m.host, m.port)

	var client *smtp.Client
	if m.port == 465 {
		conn, err := tls.Dial("tcp", addr, &tls.Config{ServerName: m.host, MinVersion: tls.VersionTLS12})
		if err != nil {
			return fmt.Errorf("smtp tls dial: %w", err)
		}
		client, err = smtp.NewClient(conn, m.host)
		if err != nil {
			return fmt.Errorf("smtp client: %w", err)
		}
	} else {
		var err error
		client, err = smtp.Dial(addr)
		if err != nil {
			return fmt.Errorf("smtp dial: %w", err)
		}
		if ok, _ := client.Extension("STARTTLS"); ok {
			if err := client.StartTLS(&tls.Config{ServerName: m.host, MinVersion: tls.VersionTLS12}); err != nil {
				return fmt.Errorf("smtp starttls: %w", err)
			}
		}
	}
	defer func() { _ = client.Close() }()

	if m.user != "" {
		auth := smtp.PlainAuth("", m.user, m.pass, m.host)
		if ok, _ := client.Extension("AUTH"); ok {
			if err := client.Auth(auth); err != nil {
				return fmt.Errorf("smtp auth: %w", err)
			}
		}
	}
	from := envelopeAddress(m.from)
	if err := client.Mail(from); err != nil {
		return fmt.Errorf("smtp mail: %w", err)
	}
	if err := client.Rcpt(to); err != nil {
		return fmt.Errorf("smtp rcpt: %w", err)
	}
	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("smtp data: %w", err)
	}
	if _, err := w.Write([]byte(msg)); err != nil {
		return fmt.Errorf("smtp write: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("smtp data close: %w", err)
	}
	return client.Quit()
}

// envelopeAddress extracts the bare address from "Name <a@b>" forms.
func envelopeAddress(from string) string {
	if i := strings.Index(from, "<"); i >= 0 {
		if j := strings.Index(from, ">"); j > i {
			return from[i+1 : j]
		}
	}
	return from
}

// buildMessage composes the RFC 5322 message with multipart/alternative.
// Every header value passes through FilterHeader (CRLF injection defense).
func buildMessage(from, to, subject, html, text string, headers map[string]string) string {
	boundary := "----core-boundary-7d2f9a"
	var b strings.Builder
	writeHeader := func(k, v string) {
		b.WriteString(k + ": " + FilterHeader(v) + "\r\n")
	}
	writeHeader("From", from)
	writeHeader("To", to)
	writeHeader("Subject", mime.QEncoding.Encode("UTF-8", FilterHeader(subject)))
	writeHeader("MIME-Version", "1.0")
	keys := make([]string, 0, len(headers))
	for k := range headers {
		keys = append(keys, k)
	}
	// Deterministic header order keeps the wire format testable.
	for i := 0; i < len(keys); i++ {
		for j := i + 1; j < len(keys); j++ {
			if keys[j] < keys[i] {
				keys[i], keys[j] = keys[j], keys[i]
			}
		}
	}
	for _, k := range keys {
		writeHeader(k, headers[k])
	}
	writeHeader("Content-Type", fmt.Sprintf("multipart/alternative; boundary=%q", boundary))
	b.WriteString("\r\n")

	b.WriteString("--" + boundary + "\r\n")
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	b.WriteString("Content-Transfer-Encoding: base64\r\n\r\n")
	b.WriteString(chunkBase64(text) + "\r\n")

	b.WriteString("--" + boundary + "\r\n")
	b.WriteString("Content-Type: text/html; charset=utf-8\r\n")
	b.WriteString("Content-Transfer-Encoding: base64\r\n\r\n")
	b.WriteString(chunkBase64(html) + "\r\n")

	b.WriteString("--" + boundary + "--\r\n")
	return b.String()
}

// chunkBase64 wraps base64 payload at 76 columns per RFC 2045.
func chunkBase64(s string) string {
	enc := base64.StdEncoding.EncodeToString([]byte(s))
	var out []string
	for len(enc) > 76 {
		out = append(out, enc[:76])
		enc = enc[76:]
	}
	out = append(out, enc)
	return strings.Join(out, "\r\n")
}
