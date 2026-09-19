package httpapi

import (
	"encoding/json"
	"errors"
	"html/template"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"github.com/iamshaynez/xiaowenz-blog-core/internal/mailer"
	"github.com/iamshaynez/xiaowenz-blog-core/internal/store"
)

// handleSubscribe implements SPEC §3.1: double opt-in with idempotent
// branches for active/pending/unsubscribed existing rows.
func (s *Server) handleSubscribe(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email          string `json:"email"`
		TurnstileToken string `json:"turnstile_token"`
		Website        string `json:"website"`
	}
	if !decodeJSONBody(w, r, &req) {
		return
	}
	if !s.guardWrite(w, r, req.Website, req.TurnstileToken, s.subLimit) {
		return
	}
	email := strings.ToLower(strings.TrimSpace(req.Email))
	addr, err := mail.ParseAddress(email)
	if err != nil || addr.Address != email || !strings.Contains(email, "@") {
		writeErr(w, http.StatusBadRequest, "invalid email")
		return
	}

	existing, err := s.store.GetSubscriberByEmail(email)
	if err == nil && existing.Status == "active" {
		// Idempotent: already subscribed, pretend success without mailing.
		writeJSON(w, http.StatusAccepted, map[string]bool{"ok": true})
		return
	}
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		s.log.Error("subscribe lookup", "err", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}

	token, err := store.NewToken()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	sub, err := s.store.UpsertPending(email, token, time.Now().Add(24*time.Hour))
	if err != nil {
		s.log.Error("subscribe upsert", "err", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}

	verifyURL := s.cfg.BaseURL + "/api/verify?token=" + sub.ConfirmToken
	html, text, err := mailer.RenderVerify(verifyURL)
	if err != nil {
		s.log.Error("render verify email", "err", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	if err := s.mailer.Send(sub.Email, "确认订阅「三杯过后」", html, text, nil); err != nil {
		s.log.Error("send verify email", "err", err, "email", sub.Email)
		writeErr(w, http.StatusBadGateway, "could not send verification email")
		return
	}
	s.log.Info("verification email sent", "email", sub.Email)
	writeJSON(w, http.StatusAccepted, map[string]bool{"ok": true})
}

// handleVerify confirms a token: activate + welcome mail + redirect (§3.1).
func (s *Server) handleVerify(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	sub, err := s.store.ConfirmSubscriber(token)
	if err != nil {
		http.Redirect(w, r, s.cfg.SiteURL+"/subscribe-expired/", http.StatusFound)
		return
	}
	unsubURL := s.cfg.BaseURL + "/api/unsub?token=" + sub.UnsubToken
	html, text, rerr := mailer.RenderWelcome(unsubURL)
	if rerr == nil {
		if serr := s.mailer.Send(sub.Email, "欢迎订阅「三杯过后」", html, text, nil); serr != nil {
			s.log.Error("send welcome email", "err", serr)
		}
	}
	s.log.Info("subscriber activated", "email", sub.Email)
	http.Redirect(w, r, s.cfg.SiteURL+"/subscribed/", http.StatusFound)
}

// handleUnsubPage renders the confirmation page (prefetch-safe GET, §3.1).
func (s *Server) handleUnsubPage(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if _, err := s.store.GetSubscriberByUnsubToken(token); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid token")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = unsubPageTemplate.Execute(w, map[string]string{"Token": token})
}

// handleUnsub executes the unsubscribe on POST only.
func (s *Server) handleUnsub(w http.ResponseWriter, r *http.Request) {
	token := r.FormValue("token")
	if token == "" {
		var body struct {
			Token string `json:"token"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		token = body.Token
	}
	if err := s.store.UnsubscribeByToken(token); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid token")
		return
	}
	s.log.Info("subscriber unsubscribed")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = unsubDoneTemplate.Execute(w, nil)
}

var unsubPageTemplate = template.Must(template.New("unsub").Parse(`<!DOCTYPE html>
<html lang="zh-CN"><head><meta charset="utf-8"><title>退订确认</title></head>
<body style="font-family:system-ui,sans-serif;max-width:480px;margin:48px auto;padding:0 16px;">
<h2>确认退订？</h2>
<p>点击下方按钮将停止接收「三杯过后」通讯。</p>
<form method="POST" action="/api/unsub">
  <input type="hidden" name="token" value="{{.Token}}">
  <button type="submit" style="padding:10px 24px;">确认退订</button>
</form>
</body></html>`))

var unsubDoneTemplate = template.Must(template.New("unsubdone").Parse(`<!DOCTYPE html>
<html lang="zh-CN"><head><meta charset="utf-8"><title>已退订</title></head>
<body style="font-family:system-ui,sans-serif;max-width:480px;margin:48px auto;padding:0 16px;">
<h2>已退订</h2>
<p>你已成功退订「三杯过后」，不会再收到群发邮件。</p>
</body></html>`))
