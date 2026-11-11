package admin

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/iamshaynez/xiaowenz-blog-core/internal/sysconfig"
)

// 系统参数（SPEC §1/§6）：媒体 R2（公开桶）与备份 R2（私有桶）都在这里配置，
// 存 settings 表。解析规则：数据库优先、逐字段回退环境变量（CORE_R2_*）——
// 老部署不用重填即可升级，后台一旦填写即覆盖环境变量。Secret 只存不回显，
// 留空表示沿用已保存值。

// settingsFlash maps query-flash codes to operator-facing messages.
var settingsFlash = map[string]string{
	"ok=media":  "媒体 R2 配置已保存",
	"ok=backup": "备份 R2 配置已保存",
	"ok=stats":  "统计站点已保存",
	"ok=bio":    "个人介绍已保存",

	"err=r2config": "配置不合法，未保存",
	"err=stats":    "统计站点不合法，未保存",
	"err=bio":      "个人介绍超长，未保存",
}

func (h *Handler) settingsPage(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	env := sysconfig.MediaR2{
		Endpoint:   h.cfg.R2Endpoint,
		KeyID:      h.cfg.R2KeyID,
		Secret:     h.cfg.R2Secret,
		Bucket:     h.cfg.R2Bucket,
		PublicBase: h.cfg.R2PublicBase,
	}
	dbMedia, err := sysconfig.LoadMediaR2(h.store)
	if err != nil {
		h.log.Warn("load media r2 config", "err", err)
	}
	eff := sysconfig.EffectiveMediaR2(dbMedia, env)
	dbBackup, err := sysconfig.LoadBackupR2(h.store)
	if err != nil {
		h.log.Warn("load backup r2 config", "err", err)
	}
	statsSites, err := sysconfig.LoadStatsSites(h.store)
	if err != nil {
		h.log.Warn("load stats sites", "err", err)
	}
	bio, err := sysconfig.LoadNewsletterBio(h.store)
	if err != nil {
		h.log.Warn("load newsletter bio", "err", err)
	}

	mediaUp := h.mediaClient != nil && h.mediaClient() != nil
	backupUp := h.backups != nil && h.backups.Configured()
	siteHost := ""
	if u, err := url.Parse(h.cfg.SiteURL); err == nil {
		siteHost = strings.ToLower(u.Hostname())
	}

	h.render(w, "settings.html", map[string]any{
		"Title":           "系统参数",
		"FlashOK":         settingsFlash["ok="+q.Get("ok")],
		"FlashErr":        settingsFlash["err="+q.Get("err")],
		"FlashDetail":     q.Get("detail"),
		"MediaDB":         dbMedia,
		"MediaEnv":        env,
		"MediaEff":        eff,
		"MediaSecretSet":  dbMedia.Secret != "" || env.Secret != "",
		"MediaUp":         mediaUp,
		"EnvAny":          env.Endpoint != "" || env.KeyID != "" || env.Secret != "" || env.Bucket != "" || env.PublicBase != "",
		"Backup":          dbBackup,
		"BackupSecretSet": dbBackup.Secret != "",
		"BackupUp":        backupUp,
		"StatsSites":      strings.Join(statsSites, "\n"),
		"StatsSiteCount":  len(statsSites),
		"SiteHost":        siteHost,
		"Bio":             bio,
		"BioEnvSet":       h.cfg.NewsletterBio != "",
	})
}

// statsSitesSave stores the multi-site stats beacon domain list. Entries are
// normalized (scheme/port stripped, lowercase, deduped); anything that is not
// a bare host rejects the whole submit — a half-parsed allowlist would fail
// silently at beacon time.
func (h *Handler) statsSitesSave(w http.ResponseWriter, r *http.Request) {
	domains := sysconfig.ParseStatsSites(r.FormValue("domains"))
	if msg := validateStatsSites(domains); msg != "" {
		h.redirect(w, r, "/admin/settings?err=stats&detail="+url.QueryEscape(msg))
		return
	}
	if err := sysconfig.SaveStatsSites(h.store, domains); err != nil {
		h.log.Error("save stats sites", "err", err)
		writeErr(w, http.StatusInternalServerError, "save failed")
		return
	}
	h.log.Info("stats sites updated", "count", len(domains))
	h.redirect(w, r, "/admin/settings?ok=stats")
}

// validateStatsSites rejects anything that is not a bare hostname.
func validateStatsSites(domains []string) string {
	for _, d := range domains {
		if strings.ContainsAny(d, "/?#:@ ") || d != "localhost" && !strings.Contains(d, ".") {
			return "每行一个纯域名（如 ask.xiaowenz.com），不要带路径/端口"
		}
	}
	return ""
}

// newsletterBioSave stores the newsletter footer bio（「我是谁 WHO AM I」
// 多行介绍）。空文本 = 清空（邮件页脚隐藏区块）；超长整单拒绝。
func (h *Handler) newsletterBioSave(w http.ResponseWriter, r *http.Request) {
	bio := strings.ReplaceAll(r.FormValue("bio"), "\r\n", "\n")
	if err := sysconfig.SaveNewsletterBio(h.store, bio); err != nil {
		h.log.Warn("newsletter bio rejected", "err", err)
		h.redirect(w, r, "/admin/settings?err=bio")
		return
	}
	h.log.Info("newsletter bio updated", "chars", len([]rune(bio)))
	h.redirect(w, r, "/admin/settings?ok=bio")
}

// mediaConfigSave stores the media R2 target. Blank secret keeps the stored
// one; the stored secret is never rendered back.
func (h *Handler) mediaConfigSave(w http.ResponseWriter, r *http.Request) {
	cur, err := sysconfig.LoadMediaR2(h.store)
	if err != nil {
		h.log.Error("load media r2 config", "err", err)
		writeErr(w, http.StatusInternalServerError, "load failed")
		return
	}
	next := sysconfig.MediaR2{
		Endpoint:   strings.TrimSpace(r.FormValue("endpoint")),
		KeyID:      strings.TrimSpace(r.FormValue("key_id")),
		Bucket:     strings.TrimSpace(r.FormValue("bucket")),
		PublicBase: strings.TrimRight(strings.TrimSpace(r.FormValue("public_base")), "/"),
	}
	if s := strings.TrimSpace(r.FormValue("secret")); s != "" {
		next.Secret = s
	} else {
		next.Secret = cur.Secret
	}
	if msg := validateMediaR2(next); msg != "" {
		h.redirect(w, r, "/admin/settings?err=r2config&detail="+url.QueryEscape(msg))
		return
	}
	if err := sysconfig.SaveMediaR2(h.store, next); err != nil {
		h.log.Error("save media r2 config", "err", err)
		writeErr(w, http.StatusInternalServerError, "save failed")
		return
	}
	h.log.Info("media R2 config updated", "bucket", next.Bucket, "endpoint", next.Endpoint) // never log secret
	h.redirect(w, r, "/admin/settings?ok=media")
}

// backupConfigSave stores the backup R2 target (moved from the backup page;
// one home for all system parameters). Blank secret keeps the stored one.
func (h *Handler) backupConfigSave(w http.ResponseWriter, r *http.Request) {
	cur, err := sysconfig.LoadBackupR2(h.store)
	if err != nil {
		h.log.Error("load backup r2 config", "err", err)
		writeErr(w, http.StatusInternalServerError, "load failed")
		return
	}
	next := sysconfig.BackupR2{
		Endpoint: strings.TrimSpace(r.FormValue("endpoint")),
		KeyID:    strings.TrimSpace(r.FormValue("key_id")),
		Bucket:   strings.TrimSpace(r.FormValue("bucket")),
	}
	if s := strings.TrimSpace(r.FormValue("secret")); s != "" {
		next.Secret = s
	} else {
		next.Secret = cur.Secret
	}
	if msg := validateBackupR2(next); msg != "" {
		h.redirect(w, r, "/admin/settings?err=r2config&detail="+url.QueryEscape(msg))
		return
	}
	if err := sysconfig.SaveBackupR2(h.store, next); err != nil {
		h.log.Error("save backup r2 config", "err", err)
		writeErr(w, http.StatusInternalServerError, "save failed")
		return
	}
	h.log.Info("backup R2 config updated", "bucket", next.Bucket, "endpoint", next.Endpoint)
	if h.backups != nil {
		h.backups.Kick()
	}
	h.redirect(w, r, "/admin/settings?ok=backup")
}

// validateMediaR2 rejects malformed values; incomplete configs are allowed
// (env fallback may complete them, the page shows what is missing).
func validateMediaR2(c sysconfig.MediaR2) string {
	if c.Endpoint != "" && !strings.HasPrefix(c.Endpoint, "https://") {
		return "媒体 endpoint 必须是 https:// 开头（形如 https://<account>.r2.cloudflarestorage.com）"
	}
	if c.PublicBase != "" && !strings.HasPrefix(c.PublicBase, "https://") {
		return "公开访问基址必须是 https:// 开头的域名"
	}
	if c.Bucket != "" && strings.ContainsAny(c.Bucket, "/ ") {
		return "媒体桶名不能包含空格或斜杠"
	}
	return ""
}

func validateBackupR2(c sysconfig.BackupR2) string {
	if c.Endpoint != "" && !strings.HasPrefix(c.Endpoint, "https://") {
		return "备份 endpoint 必须是 https:// 开头（形如 https://<account>.r2.cloudflarestorage.com）"
	}
	if c.Bucket != "" && strings.ContainsAny(c.Bucket, "/ ") {
		return "备份桶名不能包含空格或斜杠"
	}
	return ""
}
