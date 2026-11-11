// Package newsletter delivers drafts to active subscribers in a
// background goroutine, paced by CORE_SEND_INTERVAL_MS, per SPEC §4.
//
// 发送语义：每次运行只发给「选中的订阅者 ∩ active ∩ 本期尚未成功送达」，
// 同一地址绝不会收到同一期两次（send_log status='sent' 判重）。一轮结束
// 后仍有 active 订阅者未送达时回 draft 等待补发，全部送达才置 sent。
package newsletter

import (
	"fmt"
	"html/template"
	"log/slog"
	"sync"
	"time"

	"github.com/iamshaynez/xiaowenz-blog-core/internal/config"
	"github.com/iamshaynez/xiaowenz-blog-core/internal/mailer"
	"github.com/iamshaynez/xiaowenz-blog-core/internal/store"
)

// Sender fans a draft out to active subscribers, one goroutine per send run.
type Sender struct {
	store  *store.Store
	mailer mailer.Mailer
	cfg    *config.Config
	log    *slog.Logger

	mu      sync.Mutex
	running map[int64]bool
	wg      sync.WaitGroup
}

// NewSender builds a Sender.
func NewSender(st *store.Store, m mailer.Mailer, cfg *config.Config, log *slog.Logger) *Sender {
	if log == nil {
		log = slog.Default()
	}
	return &Sender{store: st, mailer: m, cfg: cfg, log: log, running: map[int64]bool{}}
}

// Start transitions a draft to sending and launches the background fan-out.
// subscriberIDs selects recipients: nil = every active subscriber not yet
// delivered this issue; non-empty = only those ids (still filtered to
// active and not-yet-sent). Starting a non-draft (or an already-running)
// newsletter is an error.
func (s *Sender) Start(id int64, subscriberIDs []int64) error {
	s.mu.Lock()
	if s.running[id] {
		s.mu.Unlock()
		return fmt.Errorf("newsletter %d already sending", id)
	}
	s.mu.Unlock()

	if _, err := s.store.GetNewsletter(id); err != nil {
		return fmt.Errorf("newsletter %d: %w", id, err)
	}
	if err := s.store.MarkNewsletterSending(id); err != nil {
		return err
	}

	s.mu.Lock()
	s.running[id] = true
	s.mu.Unlock()

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() {
			s.mu.Lock()
			delete(s.running, id)
			s.mu.Unlock()
		}()
		s.run(id, subscriberIDs)
	}()
	return nil
}

// Wait blocks until all in-flight send runs finish (shutdown + tests).
func (s *Sender) Wait() { s.wg.Wait() }

// run delivers to the selected recipients that were not yet sent this
// issue, logging every attempt, then settles the newsletter status.
func (s *Sender) run(id int64, subscriberIDs []int64) {
	n, err := s.store.GetNewsletter(id)
	if err != nil {
		s.log.Error("send: load newsletter", "id", id, "err", err)
		_ = s.store.MarkNewsletterFailed(id)
		return
	}
	subs, err := s.recipients(id, subscriberIDs)
	if err != nil {
		s.log.Error("send: list subscribers", "id", id, "err", err)
		_ = s.store.MarkNewsletterFailed(id)
		return
	}

	sent, failed := 0, 0
	for i, sub := range subs {
		if i > 0 && s.cfg.SendInterval > 0 {
			// Pace AFTER the first message: a one-recipient run need not sleep.
			sleep(s.cfg.SendInterval)
		}
		if err := s.sendOne(n, sub); err != nil {
			failed++
			s.log.Warn("send: recipient failed", "id", id, "email", sub.Email, "err", err)
			if lerr := s.store.InsertSendLog(id, sub.ID, "failed", err.Error()); lerr != nil {
				s.log.Error("send: write send_log", "err", lerr)
			}
			continue
		}
		sent++
		if err := s.store.InsertSendLog(id, sub.ID, "sent", ""); err != nil {
			s.log.Error("send: write send_log", "err", err)
		}
	}

	if err := s.settle(id, sent, failed); err != nil {
		s.log.Error("send: settle status", "id", id, "err", err)
	}
	s.log.Info("newsletter send run done", "id", id, "sent", sent, "failed", failed)
}

// recipients resolves the run's audience: active subscribers, optionally
// limited to subscriberIDs, minus everyone already sent this issue.
func (s *Sender) recipients(id int64, subscriberIDs []int64) ([]store.Subscriber, error) {
	subs, err := s.store.ActiveSubscribers()
	if err != nil {
		return nil, err
	}
	only := map[int64]bool{}
	for _, sid := range subscriberIDs {
		only[sid] = true
	}
	sent, err := s.store.SentSubscriberIDs(id)
	if err != nil {
		return nil, err
	}
	out := subs[:0]
	for _, sub := range subs {
		if len(subscriberIDs) > 0 && !only[sub.ID] {
			continue
		}
		if sent[sub.ID] {
			continue // 同一地址本期已送达，绝不重发
		}
		out = append(out, sub)
	}
	return out, nil
}

// settle closes the run: everyone delivered → sent, otherwise back to draft
// so the remaining recipients can be picked up by a follow-up run. Counts
// accumulate across runs either way.
func (s *Sender) settle(id int64, sent, failed int) error {
	states, err := s.store.SubscribersWithSendState(id)
	if err != nil {
		return err
	}
	for _, st := range states {
		if !st.Sent {
			return s.store.RevertOneToDraft(id, sent, failed)
		}
	}
	return s.store.MarkNewsletterSent(id, sent, failed)
}

// sendOne renders the per-recipient shell and delivers it.
func (s *Sender) sendOne(n *store.Newsletter, sub store.Subscriber) error {
	unsubURL := s.cfg.BaseURL + "/api/unsub?token=" + sub.UnsubToken
	html, text, err := mailer.RenderNewsletter(mailer.NewsletterShell{
		Title:          n.Subject,
		PostURL:        n.PostURL,
		BodyHTML:       template.HTML(n.HTML),
		UnsubURL:       unsubURL,
		MailingAddress: s.cfg.MailingAddress,
	})
	if err != nil {
		return fmt.Errorf("render: %w", err)
	}
	return s.mailer.Send(sub.Email, n.Subject, html, text, map[string]string{
		"List-Unsubscribe":      "<" + unsubURL + ">",
		"List-Unsubscribe-Post": "List-Unsubscribe=One-Click",
	})
}

var sleep = func(d time.Duration) { time.Sleep(d) }
