// Package mailer sends email via SMTP and renders embedded templates.
// The Mailer interface is the seam every test fakes.
package mailer

import (
	"strings"
	"sync"
)

// Mailer is the single delivery seam: HTML + text alternative + extra headers.
type Mailer interface {
	Send(to, subject, html, text string, headers map[string]string) error
}

// SentMessage is one recorded delivery (Fake).
type SentMessage struct {
	To      string
	Subject string
	HTML    string
	Text    string
	Headers map[string]string
}

// Fake is an in-memory Mailer for tests; it never touches the network.
type Fake struct {
	mu   sync.Mutex
	Sent []SentMessage
	Err  error // when set, Send returns it
}

// Send records the message (or returns Err).
func (f *Fake) Send(to, subject, html, text string, headers map[string]string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Err != nil {
		return f.Err
	}
	f.Sent = append(f.Sent, SentMessage{To: to, Subject: subject, HTML: html, Text: text, Headers: headers})
	return nil
}

// FilterHeader strips CR/LF from a header value to prevent header injection.
func FilterHeader(v string) string {
	return strings.Map(func(r rune) rune {
		if r == '\r' || r == '\n' {
			return -1
		}
		return r
	}, v)
}
