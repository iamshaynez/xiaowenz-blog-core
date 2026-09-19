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
	return &senderFixture{sender: s, store: st, fake: fm}
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

	id, err := fx.store.CreateNewsletter("第 1 期", "# md", "<h1>你好</h1>")
	if err != nil {
		t.Fatalf("CreateNewsletter: %v", err)
	}
	if err := fx.sender.Start(id); err != nil {
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
	id, _ := fx.store.CreateNewsletter("s", "m", "<p>x</p>")

	if err := fx.sender.Start(id); err != nil {
		t.Fatalf("Start: %v", err)
	}
	fx.sender.Wait()

	n, _ := fx.store.GetNewsletter(id)
	if n.Status != "sent" || n.SentCount != 1 || n.FailedCount != 1 {
		t.Errorf("newsletter = %+v, want sent 1/1", n)
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
	id, _ := fx.store.CreateNewsletter("s", "m", "h")

	if err := fx.sender.Start(id); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	// Second start on the same newsletter (now sending) must fail.
	if err := fx.sender.Start(id); err == nil {
		t.Error("Start on sending newsletter must fail")
	}
	fx.sender.Wait()
	// After sent, still not draft.
	if err := fx.sender.Start(id); err == nil {
		t.Error("Start on sent newsletter must fail")
	}
}

func TestStartUnknownNewsletter(t *testing.T) {
	fx := newSenderFixture(t, &mailer.Fake{})
	if err := fx.sender.Start(9999); err == nil {
		t.Error("Start on missing newsletter must fail")
	}
}

func TestSendZeroSubscribersStillSent(t *testing.T) {
	fx := newSenderFixture(t, &mailer.Fake{})
	id, _ := fx.store.CreateNewsletter("empty", "m", "h")
	if err := fx.sender.Start(id); err != nil {
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

func TestWaitBlocksUntilDone(t *testing.T) {
	fx := newSenderFixture(t, &mailer.Fake{})
	var emails []string
	for i := 0; i < 5; i++ {
		emails = append(emails, strings.Repeat("x", 1)+string(rune('a'+i))+"@x.com")
	}
	seedActive(t, fx.store, emails...)
	id, _ := fx.store.CreateNewsletter("s", "m", "h")
	if err := fx.sender.Start(id); err != nil {
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
