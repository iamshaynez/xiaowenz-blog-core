package admin

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/iamshaynez/xiaowenz-blog-core/internal/backup"
)

// 备份管理（SPEC §6）：快照与配置摘要进独立私有 R2 桶、cron 计划可改、
// 可手动触发、可一键测试连通。计划表达式存 settings 表，保存后立即生效；
// 保留策略固定为最近 backup.KeepDefault 份。R2 目标配置在「系统参数」页。

// flashTexts maps query-flash codes to operator-facing messages.
var backupFlash = map[string]string{
	"ok=run":      "备份完成",
	"ok=schedule": "计划已更新，下次运行已按新表达式重排",
	"ok=test":     "R2 连接正常：写入 / 列举 / 删除全部通过",

	"err=busy":         "上一份备份仍在进行中，请稍后重试",
	"err=cron":         "cron 表达式不合法（应为 5 字段，如 0 4 * * *）",
	"err=unconfigured": "备份 R2 未配置 —— 去「系统参数」页填好 endpoint / 密钥 / 桶",
	"err=failed":       "备份失败，详见服务日志",
	"err=test":         "R2 连接测试失败（配置见「系统参数」页）",
}

func (h *Handler) backupPage(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	flashOK := backupFlash["ok="+q.Get("ok")]
	flashErr := backupFlash["err="+q.Get("err")]

	data := map[string]any{
		"Title":       "备份",
		"Configured":  h.backups != nil && h.backups.Configured(),
		"Keep":        backup.KeepDefault,
		"FlashOK":     flashOK,
		"FlashErr":    flashErr,
		"FlashDetail": q.Get("detail"),
		"NextRun":     "—",
	}
	if h.backups != nil {
		data["Cron"] = h.backups.Cron()
		if next, err := h.backups.NextRun(); err == nil {
			data["NextRun"] = next.Local().Format("2006-01-02 15:04")
		}
		snaps, err := h.backups.Recent(r.Context(), 20)
		if err != nil {
			h.log.Warn("list backups", "err", err)
			data["ListError"] = err.Error()
		}
		// Present times in the server's local zone — the operator reads it.
		for i := range snaps {
			snaps[i].When = snaps[i].When.Local()
		}
		data["Snapshots"] = snaps
	} else {
		data["Cron"] = backup.DefaultCron
	}
	h.render(w, "backup.html", data)
}

// backupRun triggers one backup cycle now (serialized on the server by the
// backer's busy lock).
func (h *Handler) backupRun(w http.ResponseWriter, r *http.Request) {
	if h.backups == nil || !h.backups.Configured() {
		h.redirect(w, r, "/admin/backup?err=unconfigured")
		return
	}
	key, err := h.backups.RunOnce(r.Context())
	switch {
	case errors.Is(err, backup.ErrBusy):
		h.redirect(w, r, "/admin/backup?err=busy")
	case err != nil:
		h.log.Error("manual backup failed", "err", err)
		h.redirect(w, r, "/admin/backup?err=failed&detail="+url.QueryEscape(err.Error()))
	default:
		h.log.Info("manual backup completed", "key", key)
		h.redirect(w, r, "/admin/backup?ok=run")
	}
}

// backupSchedule validates and persists the cron expression, then kicks the
// scheduler so it applies without a restart.
func (h *Handler) backupSchedule(w http.ResponseWriter, r *http.Request) {
	expr := strings.TrimSpace(r.FormValue("cron"))
	if err := backup.ValidateCron(expr); err != nil {
		h.redirect(w, r, "/admin/backup?err=cron")
		return
	}
	if err := h.store.SetSetting(backup.SettingKey, expr); err != nil {
		h.log.Error("save backup cron", "err", err)
		writeErr(w, http.StatusInternalServerError, "save failed")
		return
	}
	if h.backups != nil {
		h.backups.Kick()
	}
	h.log.Info("backup cron updated", "cron", expr)
	h.redirect(w, r, "/admin/backup?ok=schedule")
}

// backupTest round-trips a probe object through the backup bucket.
func (h *Handler) backupTest(w http.ResponseWriter, r *http.Request) {
	if h.backups == nil || !h.backups.Configured() {
		h.redirect(w, r, "/admin/backup?err=unconfigured")
		return
	}
	if err := h.backups.TestConnection(r.Context()); err != nil {
		h.log.Warn("backup R2 test failed", "err", err)
		h.redirect(w, r, "/admin/backup?err=test&detail="+url.QueryEscape(err.Error()))
		return
	}
	h.log.Info("backup R2 test passed")
	h.redirect(w, r, "/admin/backup?ok=test")
}
