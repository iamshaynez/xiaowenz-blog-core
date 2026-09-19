package store

import (
	"testing"
)

func TestNewsletterLifecycle(t *testing.T) {
	s := newTestStore(t)

	id, err := s.CreateNewsletter("第 42 期", "# 你好\n\n正文", "<h1>你好</h1><p>正文</p>")
	if err != nil {
		t.Fatalf("CreateNewsletter: %v", err)
	}
	n, err := s.GetNewsletter(id)
	if err != nil {
		t.Fatalf("GetNewsletter: %v", err)
	}
	if n.Status != "draft" {
		t.Errorf("status = %q, want draft", n.Status)
	}

	// draft → sending.
	if err := s.MarkNewsletterSending(id); err != nil {
		t.Fatalf("MarkNewsletterSending: %v", err)
	}
	n, _ = s.GetNewsletter(id)
	if n.Status != "sending" {
		t.Errorf("status = %q, want sending", n.Status)
	}

	// send_log rows accumulate.
	sub, _ := s.UpsertPending("r@x.com", "t", futureExpiry())
	_ = s.Activate(sub.ID)
	if err := s.InsertSendLog(id, sub.ID, "sent", ""); err != nil {
		t.Fatalf("InsertSendLog: %v", err)
	}
	if err := s.InsertSendLog(id, sub.ID, "failed", "smtp down"); err != nil {
		t.Fatalf("InsertSendLog: %v", err)
	}

	// sending → sent with counts.
	if err := s.MarkNewsletterSent(id, 1, 1); err != nil {
		t.Fatalf("MarkNewsletterSent: %v", err)
	}
	n, _ = s.GetNewsletter(id)
	if n.Status != "sent" || n.SentCount != 1 || n.FailedCount != 1 {
		t.Errorf("after sent: %+v", n)
	}
	if n.SentAt == 0 {
		t.Error("sent_at not set")
	}

	logs, err := s.ListSendLog(id)
	if err != nil {
		t.Fatalf("ListSendLog: %v", err)
	}
	if len(logs) != 2 {
		t.Errorf("send_log rows = %d, want 2", len(logs))
	}
}

func TestRevertSendingOnStartup(t *testing.T) {
	s := newTestStore(t)
	id, _ := s.CreateNewsletter("s", "m", "h")
	if err := s.MarkNewsletterSending(id); err != nil {
		t.Fatalf("MarkNewsletterSending: %v", err)
	}
	reverted, err := s.RevertSendingToDraft()
	if err != nil {
		t.Fatalf("RevertSendingToDraft: %v", err)
	}
	if reverted != 1 {
		t.Errorf("reverted = %d, want 1", reverted)
	}
	n, _ := s.GetNewsletter(id)
	if n.Status != "draft" {
		t.Errorf("status = %q, want draft", n.Status)
	}
	// Idempotent: second call reverts nothing.
	reverted, _ = s.RevertSendingToDraft()
	if reverted != 0 {
		t.Errorf("second revert = %d, want 0", reverted)
	}
}

func TestListNewsletters(t *testing.T) {
	s := newTestStore(t)
	for _, subj := range []string{"one", "two"} {
		if _, err := s.CreateNewsletter(subj, "m", "h"); err != nil {
			t.Fatalf("CreateNewsletter: %v", err)
		}
	}
	list, err := s.ListNewsletters()
	if err != nil {
		t.Fatalf("ListNewsletters: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("len = %d, want 2", len(list))
	}
	// Newest first.
	if list[0].Subject != "two" {
		t.Errorf("order wrong: %+v", list)
	}
}
