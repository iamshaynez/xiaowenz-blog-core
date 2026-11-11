package newsletter

import (
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iamshaynez/xiaowenz-blog-core/internal/config"
	"github.com/iamshaynez/xiaowenz-blog-core/internal/db"
	"github.com/iamshaynez/xiaowenz-blog-core/internal/mailer"
	"github.com/iamshaynez/xiaowenz-blog-core/internal/store"
)

type senderFixture struct {
	sender *Sender
	store  *store.Store
	fake   *mailer.Fake
	cfg    *config.Config
}

func newSenderFixture(t *testing.T, m mailer.Mailer) *senderFixture {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	st := store.New(d)
	cfg := &config.Config{
		BaseURL:        "https://core.test",
		SMTPFrom:       "三杯过后 <letter@test.dev>",
		MailingAddress: "Test Road 1",
		SendInterval:   time.Millisecond,
	}
	fm, _ := m.(*mailer.Fake)
	s := NewSender(st, m, cfg, slog.New(slog.DiscardHandler))
	return &senderFixture{sender: s, store: st, fake: fm, cfg: cfg}
}

func seedActive(t *testing.T, st *store.Store, emails ...string) []store.Subscriber {
	t.Helper()
	var out []store.Subscriber
	for _, e := range emails {
		sub, err := st.UpsertPending(e, "tok", time.Now().Add(time.Hour))
		if err != nil {
			t.Fatalf("seed %s: %v", e, err)
		}
		if err := st.Activate(sub.ID); err != nil {
			t.Fatalf("activate %s: %v", e, err)
		}
		got, _ := st.GetSubscriberByEmail(e)
		out = append(out, *got)
	}
	return out
}

func TestSendToEachActiveWithPersonalUnsub(t *testing.T) {
	fx := newSenderFixture(t, &mailer.Fake{})
	subs := seedActive(t, fx.store, "a@x.com", "b@x.com")
	// Pending subscriber must NOT receive the newsletter.
	if _, err := fx.store.UpsertPending("pending@x.com", "tok", time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("seed pending: %v", err)
	}

	id, err := fx.store.CreateNewsletter("第 1 期", "# md", "<h1>你好</h1>", "")
	if err != nil {
		t.Fatalf("CreateNewsletter: %v", err)
	}
	if err := fx.sender.Start(id, nil); err != nil {
		t.Fatalf("Start: %v", err)
	}
	fx.sender.Wait()

	n, err := fx.store.GetNewsletter(id)
	if err != nil {
		t.Fatalf("GetNewsletter: %v", err)
	}
	if n.Status != "sent" || n.SentCount != 2 || n.FailedCount != 0 {
		t.Errorf("newsletter = %+v", n)
	}
	if n.SentAt == 0 {
		t.Error("sent_at not set")
	}

	if len(fx.fake.Sent) != 2 {
		t.Fatalf("emails = %d, want 2", len(fx.fake.Sent))
	}
	byTo := map[string]mailer.SentMessage{}
	for _, m := range fx.fake.Sent {
		byTo[m.To] = m
	}
	for _, sub := range subs {
		m, ok := byTo[sub.Email]
		if !ok {
			t.Errorf("no email to %s", sub.Email)
			continue
		}
		// Personal unsubscribe link in body AND List-Unsubscribe header.
		personal := "/api/unsub?token=" + sub.UnsubToken
		if !strings.Contains(m.HTML, personal) {
			t.Errorf("%s: personal unsub link missing from HTML", sub.Email)
		}
		if lu := m.Headers["List-Unsubscribe"]; !strings.Contains(lu, personal) {
			t.Errorf("%s: List-Unsubscribe = %q, want it to contain %q", sub.Email, lu, personal)
		}
		// RFC 8058 one-click unsubscribe (Gmail & co. only honor the pair).
		if lup := m.Headers["List-Unsubscribe-Post"]; lup != "List-Unsubscribe=One-Click" {
			t.Errorf("%s: List-Unsubscribe-Post = %q, want %q", sub.Email, lup, "List-Unsubscribe=One-Click")
		}
		if !strings.Contains(m.HTML, "<h1>你好</h1>") {
			t.Errorf("%s: body HTML missing", sub.Email)
		}
		if !strings.Contains(m.HTML, "三杯过后") {
			t.Errorf("%s: brand shell missing", sub.Email)
		}
		if m.Text == "" {
			t.Errorf("%s: text alternative missing", sub.Email)
		}
		if m.Subject != "第 1 期" {
			t.Errorf("%s: subject = %q", sub.Email, m.Subject)
		}
	}

	logs, err := fx.store.ListSendLog(id)
	if err != nil {
		t.Fatalf("ListSendLog: %v", err)
	}
	if len(logs) != 2 {
		t.Fatalf("send_log rows = %d, want 2", len(logs))
	}
	for _, l := range logs {
		if l.Status != "sent" {
			t.Errorf("log status = %q", l.Status)
		}
	}
}

// failOneMailer fails deliveries to one address, succeeds otherwise.
type failOneMailer struct {
	inner *mailer.Fake
	bad   string
}

func (f *failOneMailer) Send(to, subject, html, text string, headers map[string]string) error {
	if to == f.bad {
		return errors.New("smtp rejected recipient")
	}
	return f.inner.Send(to, subject, html, text, headers)
}

func TestSendPartialFailureCounted(t *testing.T) {
	fake := &mailer.Fake{}
	fx := newSenderFixture(t, &failOneMailer{inner: fake, bad: "bad@x.com"})
	seedActive(t, fx.store, "good@x.com", "bad@x.com")
	id, _ := fx.store.CreateNewsletter("s", "m", "<p>x</p>", "")

	if err := fx.sender.Start(id, nil); err != nil {
		t.Fatalf("Start: %v", err)
	}
	fx.sender.Wait()

	n, _ := fx.store.GetNewsletter(id)
	// 新语义：有收件人未送达 → 回 draft 等待重试，而不是就此置 sent。
	if n.Status != "draft" || n.SentCount != 1 || n.FailedCount != 1 {
		t.Errorf("newsletter = %+v, want draft 1/1", n)
	}
	logs, _ := fx.store.ListSendLog(id)
	var failed int
	for _, l := range logs {
		if l.Status == "failed" {
			failed++
			if l.Error == "" {
				t.Error("failed log row must carry the error")
			}
		}
	}
	if failed != 1 {
		t.Errorf("failed log rows = %d, want 1", failed)
	}
}

func TestStartRejectsNonDraft(t *testing.T) {
	fx := newSenderFixture(t, &mailer.Fake{})
	seedActive(t, fx.store, "a@x.com")
	id, _ := fx.store.CreateNewsletter("s", "m", "h", "")

	if err := fx.sender.Start(id, nil); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	// Second start on the same newsletter (now sending) must fail.
	if err := fx.sender.Start(id, nil); err == nil {
		t.Error("Start on sending newsletter must fail")
	}
	fx.sender.Wait()
	// After sent, still not draft.
	if err := fx.sender.Start(id, nil); err == nil {
		t.Error("Start on sent newsletter must fail")
	}
}

func TestStartUnknownNewsletter(t *testing.T) {
	fx := newSenderFixture(t, &mailer.Fake{})
	if err := fx.sender.Start(9999, nil); err == nil {
		t.Error("Start on missing newsletter must fail")
	}
}

func TestSendZeroSubscribersStillSent(t *testing.T) {
	fx := newSenderFixture(t, &mailer.Fake{})
	id, _ := fx.store.CreateNewsletter("empty", "m", "h", "")
	if err := fx.sender.Start(id, nil); err != nil {
		t.Fatalf("Start: %v", err)
	}
	fx.sender.Wait()
	n, _ := fx.store.GetNewsletter(id)
	if n.Status != "sent" || n.SentCount != 0 {
		t.Errorf("newsletter = %+v", n)
	}
	if len(fx.fake.Sent) != 0 {
		t.Error("no emails expected")
	}
}

// 选择性发送：只发勾选的订阅者；未选中的不收，newsletter 回 draft 等补发。
func TestStartSelectedRecipientsOnly(t *testing.T) {
	fx := newSenderFixture(t, &mailer.Fake{})
	subs := seedActive(t, fx.store, "a@x.com", "b@x.com", "c@x.com")
	id, _ := fx.store.CreateNewsletter("测试刊", "m", "<p>x</p>", "https://x.com/p")

	if err := fx.sender.Start(id, []int64{subs[0].ID}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	fx.sender.Wait()

	if len(fx.fake.Sent) != 1 || fx.fake.Sent[0].To != "a@x.com" {
		t.Fatalf("emails = %+v, want 只发 a@x.com", fx.fake.Sent)
	}
	n, _ := fx.store.GetNewsletter(id)
	if n.Status != "draft" {
		t.Errorf("部分发送后 status = %q, want draft（等待补发）", n.Status)
	}
	if n.SentCount != 1 {
		t.Errorf("SentCount = %d, want 1", n.SentCount)
	}
}

// 同一地址绝不发第二次：第二轮把已发过的 id 再传进去也会被跳过；
// 补发只针对没发过的，全部发完才置 sent。
func TestSameAddressNeverSentTwice(t *testing.T) {
	fx := newSenderFixture(t, &mailer.Fake{})
	subs := seedActive(t, fx.store, "a@x.com", "b@x.com")
	id, _ := fx.store.CreateNewsletter("补发", "m", "<p>x</p>", "")

	// 第一轮测试发送：只发 a。
	if err := fx.sender.Start(id, []int64{subs[0].ID}); err != nil {
		t.Fatalf("Start 1: %v", err)
	}
	fx.sender.Wait()
	// 第二轮：a、b 都勾选（管理员全选），a 已发过必须跳过。
	if err := fx.sender.Start(id, []int64{subs[0].ID, subs[1].ID}); err != nil {
		t.Fatalf("Start 2: %v", err)
	}
	fx.sender.Wait()

	if len(fx.fake.Sent) != 2 {
		t.Fatalf("emails = %d, want 2（a 只收一次）", len(fx.fake.Sent))
	}
	got := map[string]int{}
	for _, m := range fx.fake.Sent {
		got[m.To]++
	}
	if got["a@x.com"] != 1 || got["b@x.com"] != 1 {
		t.Errorf("按收件人计数 = %v, want a:1 b:1（a 不应收到第二封）", got)
	}
	n, _ := fx.store.GetNewsletter(id)
	if n.Status != "sent" || n.SentCount != 2 {
		t.Errorf("newsletter = %+v, want sent/2", n)
	}
}

// failed 的收件人不算「已发送」：留在待发送集合里，可再发起重试。
type failFirstMailer struct {
	inner *mailer.Fake
	bad   string
	done  bool
}

func (f *failFirstMailer) Send(to, subject, html, text string, headers map[string]string) error {
	if to == f.bad && !f.done {
		f.done = true
		return errors.New("temporary smtp failure")
	}
	return f.inner.Send(to, subject, html, text, headers)
}

func TestFailedRecipientRetriedOnNextRun(t *testing.T) {
	fake := &mailer.Fake{}
	fx := newSenderFixture(t, &failFirstMailer{inner: fake, bad: "bad@x.com"})
	seedActive(t, fx.store, "good@x.com", "bad@x.com")
	id, _ := fx.store.CreateNewsletter("重试", "m", "<p>x</p>", "")

	if err := fx.sender.Start(id, nil); err != nil {
		t.Fatalf("Start 1: %v", err)
	}
	fx.sender.Wait()
	n, _ := fx.store.GetNewsletter(id)
	if n.Status != "draft" || n.SentCount != 1 || n.FailedCount != 1 {
		t.Fatalf("第一轮后 = %+v, want draft 1/1", n)
	}

	// 第二轮：good 已发过被跳过，bad 重试成功 → 全部送达。
	if err := fx.sender.Start(id, nil); err != nil {
		t.Fatalf("Start 2: %v", err)
	}
	fx.sender.Wait()
	n, _ = fx.store.GetNewsletter(id)
	if n.Status != "sent" || n.SentCount != 2 || n.FailedCount != 1 {
		t.Errorf("第二轮后 = %+v, want sent 2/1（计数累加）", n)
	}
	if len(fake.Sent) != 2 {
		t.Errorf("emails = %d, want 2（good 一次 + bad 一次）", len(fake.Sent))
	}
}

// 传入的 id 里有 pending/不存在的订阅者：静默过滤，不报错不发送。
func TestStartFiltersInactiveAndUnknownIDs(t *testing.T) {
	fx := newSenderFixture(t, &mailer.Fake{})
	a := seedActive(t, fx.store, "a@x.com")[0]
	pending, _ := fx.store.UpsertPending("p@x.com", "tok", time.Now().Add(time.Hour))
	id, _ := fx.store.CreateNewsletter("s", "m", "h", "")

	if err := fx.sender.Start(id, []int64{a.ID, pending.ID, 99999}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	fx.sender.Wait()
	if len(fx.fake.Sent) != 1 || fx.fake.Sent[0].To != "a@x.com" {
		t.Errorf("emails = %+v, want 只发 active 的 a@x.com", fx.fake.Sent)
	}
}

// 邮件壳带上文章标题与「阅读原文」链接（全文邮件排版）。
func TestSendOneCarriesArticleShell(t *testing.T) {
	fx := newSenderFixture(t, &mailer.Fake{})
	seedActive(t, fx.store, "a@x.com")
	id, _ := fx.store.CreateNewsletter("文章标题", "m", "<p>正文</p>", "https://x.com/p.html")

	if err := fx.sender.Start(id, nil); err != nil {
		t.Fatalf("Start: %v", err)
	}
	fx.sender.Wait()
	if len(fx.fake.Sent) != 1 {
		t.Fatalf("emails = %d", len(fx.fake.Sent))
	}
	m := fx.fake.Sent[0]
	if !strings.Contains(m.HTML, "文章标题") {
		t.Error("邮件缺文章大标题")
	}
	if !strings.Contains(m.HTML, "阅读原文") || !strings.Contains(m.HTML, "https://x.com/p.html") {
		t.Error("邮件缺「阅读原文」链接")
	}
	if !strings.Contains(m.Text, "正文") {
		t.Error("text 备选缺正文")
	}
}

func TestWaitBlocksUntilDone(t *testing.T) {
	fx := newSenderFixture(t, &mailer.Fake{})
	var emails []string
	for i := 0; i < 5; i++ {
		emails = append(emails, strings.Repeat("x", 1)+string(rune('a'+i))+"@x.com")
	}
	seedActive(t, fx.store, emails...)
	id, _ := fx.store.CreateNewsletter("s", "m", "h", "")
	if err := fx.sender.Start(id, nil); err != nil {
		t.Fatalf("Start: %v", err)
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); fx.sender.Wait() }()
	wg.Wait() // must return only after completion
	n, _ := fx.store.GetNewsletter(id)
	if n.Status != "sent" {
		t.Errorf("Wait returned before completion: %+v", n)
	}
}

// 发送的邮件带页脚 bio（数据库优先，env 兜底——fixture 走 env 路径）。
func TestSendCarriesFooterBio(t *testing.T) {
	fx := newSenderFixture(t, &mailer.Fake{})
	fx.cfg.NewsletterBio = "发件人的介绍。\n第二行。"
	seedActive(t, fx.store, "a@x.com")
	id, _ := fx.store.CreateNewsletter("t", "m", "<p>x</p>", "")

	if err := fx.sender.Start(id, nil); err != nil {
		t.Fatalf("Start: %v", err)
	}
	fx.sender.Wait()
	if len(fx.fake.Sent) != 1 {
		t.Fatalf("emails = %d", len(fx.fake.Sent))
	}
	m := fx.fake.Sent[0]
	if !strings.Contains(m.HTML, "我是谁 WHO AM I") || !strings.Contains(m.HTML, "发件人的介绍。<br>第二行。") {
		t.Error("HTML 版缺 bio 区块")
	}
	if !strings.Contains(m.Text, "发件人的介绍。\n第二行。") {
		t.Error("text 版缺 bio 原文")
	}
}
