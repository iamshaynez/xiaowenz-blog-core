// Package newsletter delivers drafts to all active subscribers in a
// background goroutine, paced by CORE_SEND_INTERVAL_MS, per SPEC §4.
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
// Starting a non-draft (or an already-running) newsletter is an error.
func (s *Sender) Start(id int64) error {
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
		s.run(id)
	}()
	return nil
}

// Wait blocks until all in-flight send runs finish (shutdown + tests).
func (s *Sender) Wait() { s.wg.Wait() }

// run delivers to each active subscriber, logging every attempt.
func (s *Sender) run(id int64) {
	n, err := s.store.GetNewsletter(id)
	if err != nil {
		s.log.Error("send: load newsletter", "id", id, "err", err)
		_ = s.store.MarkNewsletterFailed(id)
		return
	}
	subs, err := s.store.ActiveSubscribers()
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

	if err := s.store.MarkNewsletterSent(id, sent, failed); err != nil {
		s.log.Error("send: mark sent", "id", id, "err", err)
		return
	}
	s.log.Info("newsletter sent", "id", id, "sent", sent, "failed", failed)
}

// sendOne renders the per-recipient shell and delivers it.
func (s *Sender) sendOne(n *store.Newsletter, sub store.Subscriber) error {
	unsubURL := s.cfg.BaseURL + "/api/unsub?token=" + sub.UnsubToken
	html, text, err := mailer.RenderNewsletter(mailer.NewsletterShell{
		BodyHTML:       template.HTML(n.HTML),
		UnsubURL:       unsubURL,
		MailingAddress: s.cfg.MailingAddress,
	})
	if err != nil {
		return fmt.Errorf("render: %w", err)
	}
	return s.mailer.Send(sub.Email, n.Subject, html, text, map[string]string{
		"List-Unsubscribe": "<" + unsubURL + ">",
	})
}

var sleep = func(d time.Duration) { time.Sleep(d) }
