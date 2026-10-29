package store

import (
	"testing"
	"time"
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

// DeleteNewsletter：draft 可删；sending 在途拒删；sent 连带清理 send_log。
func TestDeleteNewsletter(t *testing.T) {
	s := newTestStore(t)

	// draft → 可删
	id, err := s.CreateNewsletter("删我", "md", "<p>md</p>")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteNewsletter(id); err != nil {
		t.Fatalf("draft 删除失败: %v", err)
	}
	if list, _ := s.ListNewsletters(); len(list) != 0 {
		t.Errorf("删除后列表应空: %+v", list)
	}

	// sending → 在途拒删
	id2, _ := s.CreateNewsletter("发送中", "md", "<p>x</p>")
	if err := s.MarkNewsletterSending(id2); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteNewsletter(id2); err == nil {
		t.Error("sending 状态必须拒删（在途群发不可中断为删除）")
	}

	// sent → 删除连带 send_log（subscriber_id 有外键，先造真实订阅者）
	id3, _ := s.CreateNewsletter("已发", "md", "<p>x</p>")
	sub1, _ := s.UpsertPending("a@x.com", "tok-a", time.Now().Add(time.Hour))
	sub2, _ := s.UpsertPending("b@x.com", "tok-b", time.Now().Add(time.Hour))
	_ = s.MarkNewsletterSending(id3)
	_ = s.MarkNewsletterSent(id3, 3, 0)
	if err := s.InsertSendLog(id3, sub1.ID, "sent", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertSendLog(id3, sub2.ID, "sent", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteNewsletter(id3); err != nil {
		t.Fatalf("sent 删除失败: %v", err)
	}
	logs, _ := s.ListSendLog(id3)
	if len(logs) != 0 {
		t.Errorf("send_log 应级联清理，剩 %d 条", len(logs))
	}
}
