package store

import (
	"testing"
	"time"
)

func TestNewsletterLifecycle(t *testing.T) {
	s := newTestStore(t)

	id, err := s.CreateNewsletter("第 42 期", "# 你好\n\n正文", "<h1>你好</h1><p>正文</p>", "")
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
	id, _ := s.CreateNewsletter("s", "m", "h", "")
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
		if _, err := s.CreateNewsletter(subj, "m", "h", ""); err != nil {
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

// 部分发送语义：一轮发送结束后若还有订阅者未送达，计数累加并回 draft
// 等待补发；全部发完才置 sent。计数在多轮之间是累加而非覆盖。
func TestPartialSendAccumulatesAndReverts(t *testing.T) {
	s := newTestStore(t)
	id, err := s.CreateNewsletter("部分发送", "md", "<p>x</p>", "https://x.com/a")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.MarkNewsletterSending(id); err != nil {
		t.Fatal(err)
	}
	// 第一轮（测试发送）：2 送达 1 失败 → 回 draft，计数已累计。
	if err := s.RevertOneToDraft(id, 2, 1); err != nil {
		t.Fatalf("RevertOneToDraft: %v", err)
	}
	n, _ := s.GetNewsletter(id)
	if n.Status != "draft" {
		t.Errorf("partial run 后 status = %q, want draft", n.Status)
	}
	if n.SentCount != 2 || n.FailedCount != 1 {
		t.Errorf("partial run 后 counts = %d/%d, want 2/1", n.SentCount, n.FailedCount)
	}
	// 第二轮（补发）：3 送达 → sent，计数在上一轮基础上累加。
	if err := s.MarkNewsletterSending(id); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkNewsletterSent(id, 3, 0); err != nil {
		t.Fatal(err)
	}
	n, _ = s.GetNewsletter(id)
	if n.Status != "sent" {
		t.Errorf("final run 后 status = %q, want sent", n.Status)
	}
	if n.SentCount != 5 || n.FailedCount != 1 {
		t.Errorf("final counts = %d/%d, want 5/1（累加）", n.SentCount, n.FailedCount)
	}
	if n.SentAt == 0 {
		t.Error("sent_at not set")
	}
}

// RevertOneToDraft 只接受 sending 在途状态，draft/sent 一律报错。
func TestRevertOneToDraftGuardsState(t *testing.T) {
	s := newTestStore(t)
	id, _ := s.CreateNewsletter("s", "m", "h", "")
	if err := s.RevertOneToDraft(id, 1, 0); err == nil {
		t.Error("draft 状态调用 RevertOneToDraft 必须报错")
	}
	_ = s.MarkNewsletterSending(id)
	if err := s.MarkNewsletterSent(id, 1, 0); err != nil {
		t.Fatal(err)
	}
	if err := s.RevertOneToDraft(id, 1, 0); err == nil {
		t.Error("sent 状态调用 RevertOneToDraft 必须报错")
	}
}

// SubscribersWithSendState：只列 active 订阅者并标注本期是否已送达
// （仅 status='sent' 算已发，failed 可重试；已退订/待验证不出现在收件人里）。
func TestSubscribersWithSendState(t *testing.T) {
	s := newTestStore(t)
	nl, _ := s.CreateNewsletter("判重", "m", "h", "")
	a, _ := s.UpsertPending("a@x.com", "ta", futureExpiry())
	b, _ := s.UpsertPending("b@x.com", "tb", futureExpiry())
	c, _ := s.UpsertPending("c@x.com", "tc", futureExpiry())
	d, _ := s.UpsertPending("d@x.com", "td", futureExpiry())
	for _, sub := range []struct{ id int64 }{{a.ID}, {b.ID}, {c.ID}, {d.ID}} {
		if err := s.Activate(sub.id); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.UnsubscribeByToken(c.UnsubToken); err != nil {
		t.Fatal(err)
	}
	_ = s.InsertSendLog(nl, a.ID, "sent", "")
	_ = s.InsertSendLog(nl, a.ID, "sent", "") // 重试留下的重复行不算多次
	_ = s.InsertSendLog(nl, b.ID, "failed", "smtp down")
	_ = s.InsertSendLog(nl, c.ID, "sent", "") // c 已退订，不应出现

	got, err := s.SubscribersWithSendState(nl)
	if err != nil {
		t.Fatalf("SubscribersWithSendState: %v", err)
	}
	if len(got) != 3 { // a, b, d（c 退订、无 pending 之外的干扰）
		t.Fatalf("len = %d, want 3: %+v", len(got), got)
	}
	byMail := map[string]bool{}
	for _, row := range got {
		byMail[row.Subscriber.Email] = row.Sent
	}
	if !byMail["a@x.com"] {
		t.Error("a 已 sent，应标 true")
	}
	if byMail["b@x.com"] {
		t.Error("b 仅 failed，应标 false（可重试）")
	}
	if byMail["d@x.com"] {
		t.Error("d 未发送，应标 false")
	}
}

// SentSubscriberIDs：已送达集合（send_log status='sent'），failed 不进集合。
func TestSentSubscriberIDs(t *testing.T) {
	s := newTestStore(t)
	nl, _ := s.CreateNewsletter("集合", "m", "h", "")
	a, _ := s.UpsertPending("a@x.com", "ta", futureExpiry())
	b, _ := s.UpsertPending("b@x.com", "tb", futureExpiry())
	_ = s.Activate(a.ID)
	_ = s.Activate(b.ID)
	_ = s.InsertSendLog(nl, a.ID, "sent", "")
	_ = s.InsertSendLog(nl, b.ID, "failed", "boom")

	sent, err := s.SentSubscriberIDs(nl)
	if err != nil {
		t.Fatalf("SentSubscriberIDs: %v", err)
	}
	if len(sent) != 1 || !sent[a.ID] {
		t.Errorf("sent = %v, want {%d}", sent, a.ID)
	}
}

// DeleteNewsletter：draft 可删；sending 在途拒删；sent 连带清理 send_log。
func TestDeleteNewsletter(t *testing.T) {
	s := newTestStore(t)

	// draft → 可删
	id, err := s.CreateNewsletter("删我", "md", "<p>md</p>", "")
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
	id2, _ := s.CreateNewsletter("发送中", "md", "<p>x</p>", "")
	if err := s.MarkNewsletterSending(id2); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteNewsletter(id2); err == nil {
		t.Error("sending 状态必须拒删（在途群发不可中断为删除）")
	}

	// sent → 删除连带 send_log（subscriber_id 有外键，先造真实订阅者）
	id3, _ := s.CreateNewsletter("已发", "md", "<p>x</p>", "")
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
