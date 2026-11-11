package admin

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"html/template"
	"net/http"
	"path"
	"strconv"
	"strings"

	"github.com/iamshaynez/xiaowenz-blog-core/internal/mailer"
	"github.com/iamshaynez/xiaowenz-blog-core/internal/sysconfig"
	"github.com/yuin/goldmark"
)

// newslettersPage lists all newsletters, newest first.
func (h *Handler) newslettersPage(w http.ResponseWriter, r *http.Request) {
	list, err := h.store.ListNewsletters()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "query failed")
		return
	}
	h.render(w, "newsletters.html", map[string]any{"Title": "Newsletter", "List": list})
}

// newsletterNew renders the compose form; recent posts from the blog feed
// (best-effort) become a dropdown so drafts need zero typing.
func (h *Handler) newsletterNew(w http.ResponseWriter, r *http.Request) {
	h.render(w, "newsletter_new.html", map[string]any{"Title": "新建 Newsletter", "Posts": h.blogPosts()})
}

// newsletterCreate renders markdown with goldmark and stores the draft.
func (h *Handler) newsletterCreate(w http.ResponseWriter, r *http.Request) {
	subject := strings.TrimSpace(r.FormValue("subject"))
	markdown := strings.TrimSpace(r.FormValue("markdown"))
	if subject == "" || markdown == "" {
		writeErr(w, http.StatusBadRequest, "subject and markdown required")
		return
	}
	var buf bytes.Buffer
	if err := goldmark.Convert([]byte(markdown), &buf); err != nil {
		writeErr(w, http.StatusBadRequest, "markdown render failed")
		return
	}
	// 手动草稿同样邮件化（注入 inline style；无来源 URL，不做绝对化）。
	id, err := h.store.CreateNewsletter(subject, markdown, mailer.EmailizeHTML(buf.String(), ""), "")
	if err != nil {
		h.log.Error("create newsletter", "err", err)
		writeErr(w, http.StatusInternalServerError, "create failed")
		return
	}
	h.log.Info("newsletter draft created", "id", id)
	h.redirect(w, r, "/admin/newsletters/"+strconv.FormatInt(id, 10))
}

// newsletterView renders the draft review page. draft 状态附收件人选择器
// 数据（active 订阅者 + 本期送达状态）；预览交给 /preview iframe。
func (h *Handler) newsletterView(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad id")
		return
	}
	n, err := h.store.GetNewsletter(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	data := map[string]any{"Title": n.Subject, "N": n}
	if n.Status == "draft" {
		recips, err := h.store.SubscribersWithSendState(id)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "query failed")
			return
		}
		sent := 0
		for _, rc := range recips {
			if rc.Sent {
				sent++
			}
		}
		data["Recipients"] = recips
		data["RcptTotal"] = len(recips)
		data["RcptSent"] = sent
		data["RcptPending"] = len(recips) - sent
	}
	h.render(w, "newsletter_view.html", data)
}

// newsletterPreview 渲染订阅者视角的完整邮件（与发送同一壳模板，
// 退订链接用 # 占位），供草稿页 iframe 所见即所得预览。
func (h *Handler) newsletterPreview(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad id")
		return
	}
	n, err := h.store.GetNewsletter(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	bio, err := sysconfig.LoadNewsletterBio(h.store)
	if err != nil {
		h.log.Warn("load newsletter bio", "err", err)
		bio = ""
	}
	html, _, err := mailer.RenderNewsletter(mailer.NewsletterShell{
		Title:          n.Subject,
		PostURL:        n.PostURL,
		BodyHTML:       template.HTML(n.HTML), // trusted: admin-authored
		UnsubURL:       "#",
		MailingAddress: h.cfg.MailingAddress,
		Bio:            sysconfig.EffectiveNewsletterBio(bio, h.cfg.NewsletterBio),
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "render failed")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(html))
}

// newsletterDelete removes a draft/sent newsletter (cascades send_log);
// sending（在途）拒删。
func (h *Handler) newsletterDelete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad id")
		return
	}
	if err := h.store.DeleteNewsletter(id); err != nil {
		if strings.Contains(err.Error(), "sending") {
			writeErr(w, http.StatusConflict, "sending in flight")
			return
		}
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	h.log.Info("newsletter deleted", "id", id)
	h.redirect(w, r, "/admin/newsletters")
}

// newsletterSend kicks off the background fan-out to the checked
// recipients (form field ids). 已发送过的邮箱由 Sender 判重跳过；
// 一个都没勾直接拒绝。
func (h *Handler) newsletterSend(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad id")
		return
	}
	if err := r.ParseForm(); err != nil {
		writeErr(w, http.StatusBadRequest, "bad form")
		return
	}
	var ids []int64
	for _, v := range r.PostForm["ids"] {
		if sid, err := strconv.ParseInt(v, 10, 64); err == nil {
			ids = append(ids, sid)
		}
	}
	if len(ids) == 0 {
		writeErr(w, http.StatusBadRequest, "no recipients selected")
		return
	}
	if err := h.sender.Start(id, ids); err != nil {
		h.log.Warn("newsletter send rejected", "id", id, "err", err)
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	h.log.Info("newsletter send started", "id", id, "recipients", len(ids))
	h.redirect(w, r, "/admin/newsletters/"+strconv.FormatInt(id, 10))
}

const maxUploadBytes = 100 << 20 // 100MB per SPEC §4

// mediaUpload accepts a multipart file, pushes it to R2 under images/ or
// audio/ (mime whitelist), records the media row, returns {url,bytes,key}.
func (h *Handler) mediaUpload(w http.ResponseWriter, r *http.Request) {
	client := h.mediaClient
	if client == nil {
		h.log.Warn("media upload rejected: R2 not configured")
		writeErr(w, http.StatusServiceUnavailable, "media storage not configured")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, h.maxUpload)
	file, header, err := r.FormFile("file")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "file required (max 100MB)")
		return
	}
	defer func() { _ = file.Close() }()

	contentType := header.Header.Get("Content-Type")
	prefix := ""
	switch {
	case contentType == "image/svg" || contentType == "image/svg+xml":
		// SVG matches image/* but executes scripts on the media origin —
		// a stored-XSS carrier, rejected by name.
		writeErr(w, http.StatusBadRequest, "svg uploads are not allowed")
		return
	case strings.HasPrefix(contentType, "image/"):
		prefix = "images/"
	case contentType == "audio/mpeg" || contentType == "audio/mp4":
		prefix = "audio/"
	default:
		writeErr(w, http.StatusBadRequest, "unsupported content type "+contentType)
		return
	}

	filename := sanitizeFilename(header.Filename)
	if filename == "" {
		filename = "upload"
	}
	key := prefix + filename
	// Conflict → short random suffix before the extension.
	if _, err := h.store.GetMediaByKey(key); err == nil {
		suffix, _ := shortRand()
		ext := path.Ext(filename)
		key = prefix + strings.TrimSuffix(filename, ext) + "-" + suffix + ext
	}

	url, err := client().Put(r.Context(), key, file, header.Size, contentType)
	if err != nil {
		h.log.Error("s3 put", "err", err, "key", key)
		writeErr(w, http.StatusBadGateway, "upload failed")
		return
	}
	if err := h.store.CreateMedia(key, url, header.Size, contentType); err != nil {
		h.log.Error("create media row", "err", err, "key", key)
		writeErr(w, http.StatusInternalServerError, "record failed")
		return
	}
	h.log.Info("media uploaded", "key", key, "bytes", header.Size)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{"url": url, "bytes": header.Size, "key": key})
}

// sanitizeFilename keeps letters, digits, dash, underscore and dot.
func sanitizeFilename(name string) string {
	name = path.Base(strings.ReplaceAll(name, "\\", "/"))
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_', r == '.':
			b.WriteRune(r)
		}
	}
	return strings.Trim(b.String(), ".")
}

func shortRand() (string, error) {
	b := make([]byte, 3)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// mediaPage lists uploaded objects.
func (h *Handler) mediaPage(w http.ResponseWriter, r *http.Request) {
	media, err := h.store.ListMedia()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "query failed")
		return
	}
	h.render(w, "media.html", map[string]any{
		"Title": "媒体库", "Media": media, "R2": h.mediaClient != nil && h.mediaClient() != nil,
		"Podcast": h.podcastCoherenceFor(media),
	})
}
