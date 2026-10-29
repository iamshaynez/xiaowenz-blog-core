package admin

import (
	"embed"
	"fmt"
	"html/template"
	"net/http"
	"strings"
	"time"
)

//go:embed templates/*.html
var templatesFS embed.FS

var tpl = template.Must(template.New("").Funcs(template.FuncMap{
	"add":     func(a, b int) int { return a + b },
	"isImage": func(ct string) bool { return strings.HasPrefix(ct, "image/") },
	"rfc3339": func(unix int64) string {
		if unix == 0 {
			return "-"
		}
		return time.Unix(unix, 0).UTC().Format("2006-01-02 15:04")
	},
	"backupWhen": func(t time.Time) string { return t.Format("2006-01-02 15:04:05") },
	// sourceBadge labels where a settings field's effective value comes from:
	// console database first, environment fallback second (sysconfig rule).
	"sourceBadge": func(db, env string) template.HTML {
		switch {
		case db != "":
			return ` <span class="badge ok">数据库</span>`
		case env != "":
			return ` <span class="badge pending">环境变量</span>`
		default:
			return ` <span class="badge bad">未设置</span>`
		}
	},
	"fmtBytes": func(n int64) string {
		switch {
		case n >= 1<<20:
			return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
		case n >= 1<<10:
			return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
		default:
			return fmt.Sprintf("%d B", n)
		}
	},
}).ParseFS(templatesFS, "templates/*.html"))

// render executes a page template; all user data passes through html/template
// escaping (AGENTS §4.4).
func (h *Handler) render(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tpl.ExecuteTemplate(w, name, data); err != nil {
		h.log.Error("render template", "name", name, "err", err)
		writeErr(w, http.StatusInternalServerError, "render error")
	}
}
