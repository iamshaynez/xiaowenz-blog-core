package mailer

import (
	"bytes"
	"context"
	"log/slog"
	"testing"
)

func TestLogMailerSucceedsAndLogs(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	m := NewLogMailer(log)
	if err := m.Send("a@x.com", "主题", "<p>h</p>", "h", map[string]string{"List-Unsubscribe": "<u>"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"a@x.com", "主题", "Unsubscribe"} {
		if !bytes.Contains([]byte(out), []byte(want)) {
			t.Errorf("log output missing %q: %s", want, out)
		}
	}
	// Implements Mailer.
	var _ Mailer = m
	_ = context.Background()
}
