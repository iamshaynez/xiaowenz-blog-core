package httpapi

import (
	"net/http"
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/iamshaynez/xiaowenz-blog-core/internal/mailer"
)

// handleCreateComment validates and queues a comment for moderation, then
// notifies the admin with signed one-click links (SPEC §3.2).
func (s *Server) handleCreateComment(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Post           string `json:"post"`
		Name           string `json:"name"`
		Email          string `json:"email"`
		Content        string `json:"content"`
		TurnstileToken string `json:"turnstile_token"`
		Website        string `json:"website"`
	}
	if !decodeJSONBody(w, r, &req) {
		return
	}
	if !s.guardWrite(w, r, req.Website, req.TurnstileToken, s.comLimit) {
		return
	}

	req.Post = strings.TrimSpace(req.Post)
	req.Name = strings.TrimSpace(req.Name)
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))
	req.Content = strings.TrimSpace(req.Content)

	if req.Post == "" {
		writeErr(w, http.StatusBadRequest, "post required")
		return
	}
	if n := utf8.RuneCountInString(req.Name); n < 1 || n > 50 {
		writeErr(w, http.StatusBadRequest, "name must be 1-50 chars")
		return
	}
	addr, err := mail.ParseAddress(req.Email)
	if err != nil || addr.Address != req.Email {
		writeErr(w, http.StatusBadRequest, "invalid email")
		return
	}
	if n := utf8.RuneCountInString(req.Content); n < 1 || n > 2000 {
		writeErr(w, http.StatusBadRequest, "content must be 1-2000 chars")
		return
	}

	id, err := s.store.CreateComment(req.Post, req.Name, req.Email, req.Content,
		s.ipHash(r), r.UserAgent())
	if err != nil {
		s.log.Error("create comment", "err", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	s.notifyAdminComment(id, req.Post, req.Name, req.Content)
	writeJSON(w, http.StatusAccepted, map[string]bool{"ok": true})
}

// notifyAdminComment mails the admin with signed approve/reject links.
// Failures are logged, never fatal to the commenter.
func (s *Server) notifyAdminComment(id int64, post, name, content string) {
	to := adminNotifyAddress(s.cfg.SMTPFrom)
	if to == "" {
		s.log.Warn("admin comment notify skipped: no recipient derivable from CORE_SMTP_FROM",
			"comment_id", id, "post", post)
		return
	}
	base := s.cfg.BaseURL + "/api/comments/moderate?token="
	html, text, err := mailer.RenderCommentNotify(mailer.CommentNotice{
		PostSlug:   post,
		AuthorName: name,
		Content:    content,
		ApproveURL: base + s.signModerate(id, "approve"),
		RejectURL:  base + s.signModerate(id, "reject"),
	})
	if err != nil {
		s.log.Error("render comment notify", "err", err)
		return
	}
	if err := s.mailer.Send(to, "新评论待审核 · "+post, html, text, nil); err != nil {
		s.log.Error("send comment notify", "err", err)
	}
}

// adminNotifyAddress derives the admin mailbox from CORE_SMTP_FROM
// (documented conservative choice; SPEC defines no CORE_ADMIN_EMAIL).
func adminNotifyAddress(from string) string {
	addr, err := mail.ParseAddress(from)
	if err != nil {
		return from
	}
	return addr.Address
}

// handleListComments returns approved comments for a post (public projection).
func (s *Server) handleListComments(w http.ResponseWriter, r *http.Request) {
	post := r.URL.Query().Get("post")
	if post == "" {
		writeErr(w, http.StatusBadRequest, "post required")
		return
	}
	comments, err := s.store.ListApprovedComments(post)
	if err != nil {
		s.log.Error("list comments", "err", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	type pub struct {
		AuthorName string `json:"author_name"`
		Content    string `json:"content"`
		CreatedAt  string `json:"created_at"`
	}
	out := make([]pub, 0, len(comments))
	for _, c := range comments {
		out = append(out, pub{
			AuthorName: c.AuthorName,
			Content:    c.Content,
			CreatedAt:  time.Unix(c.CreatedAt, 0).UTC().Format(time.RFC3339),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"comments": out})
}

// handleModerate executes the signed one-click approve/reject from the admin
// notification email.
func (s *Server) handleModerate(w http.ResponseWriter, r *http.Request) {
	id, action, ok := s.verifyModerate(r.URL.Query().Get("token"))
	if !ok {
		writeErr(w, http.StatusForbidden, "invalid token")
		return
	}
	status := "approved"
	if action == "reject" {
		status = "rejected"
	}
	if err := s.store.ModerateComment(id, status); err != nil {
		s.log.Error("moderate comment", "err", err, "id", id)
		writeErr(w, http.StatusBadRequest, "moderation failed")
		return
	}
	s.log.Info("comment moderated", "id", id, "status", status)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	msg := "评论已通过。"
	if action == "reject" {
		msg = "评论已拒绝。"
	}
	_, _ = w.Write([]byte(`<!DOCTYPE html><html lang="zh-CN"><head><meta charset="utf-8"><title>审核完成</title></head>
<body style="font-family:system-ui,sans-serif;max-width:480px;margin:48px auto;"><h2>` + msg + `</h2></body></html>`))
}
