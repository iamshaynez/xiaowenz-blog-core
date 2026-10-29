package admin

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"github.com/iamshaynez/xiaowenz-blog-core/internal/store"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// dashboard shows the §4 headline numbers.
func (h *Handler) dashboard(w http.ResponseWriter, r *http.Request) {
	since := time.Now().Add(-30 * 24 * time.Hour).Unix()
	active, err1 := h.store.CountSubscribers("active", "")
	pending, err2 := h.store.CountComments("pending")
	pv, uv, err3 := h.store.PageviewStats(since)
	downloads, err4 := h.store.DownloadStats(since)
	recent, err5 := h.store.ListComments("pending")
	for _, err := range []error{err1, err2, err3, err4, err5} {
		if err != nil {
			h.log.Error("dashboard query", "err", err)
			writeErr(w, http.StatusInternalServerError, "query failed")
			return
		}
	}
	rows := sortedDownloadRows(downloads)
	if len(recent) > 5 {
		recent = recent[:5]
	}
	h.render(w, "dashboard.html", map[string]any{
		"Title":           "仪表盘",
		"ActiveSubs":      active,
		"PendingComments": pending,
		"PV30":            pv,
		"UV30":            uv,
		"Downloads":       rows,
		"RecentPending":   recent,
	})
}

// subscribersPage lists with status/q filters, 50 per page.
func (h *Handler) subscribersPage(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	status, search := q.Get("status"), q.Get("q")
	page, _ := strconv.Atoi(q.Get("page"))
	if page < 1 {
		page = 1
	}
	subs, err := h.store.ListSubscribers(status, search, (page-1)*50, 50)
	if err != nil {
		h.log.Error("list subscribers", "err", err)
		writeErr(w, http.StatusInternalServerError, "query failed")
		return
	}
	total, err := h.store.CountSubscribers(status, search)
	if err != nil {
		h.log.Error("count subscribers", "err", err)
		writeErr(w, http.StatusInternalServerError, "query failed")
		return
	}
	counts := map[string]int{}
	for _, st := range []string{"active", "pending", "unsubscribed"} {
		if n, err := h.store.CountSubscribers(st, ""); err == nil {
			counts[st] = n
		}
	}
	h.render(w, "subscribers.html", map[string]any{
		"Title": "订阅名单",
		"Subs":  subs, "Total": total, "Page": page,
		"Status": status, "Q": search, "HasMore": page*50 < total,
		"Imported": q.Get("imported"), "Skipped": q.Get("skipped"), "Invalid": q.Get("invalid"),
		"Counts": counts,
	})
}

// importSubscribers ingests a CSV (columns: email[,confirmed]); every row is
// activated immediately — the list comes from an already-verified platform.
func (h *Handler) importSubscribers(w http.ResponseWriter, r *http.Request) {
	file, _, err := r.FormFile("file")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "CSV file required")
		return
	}
	defer func() { _ = file.Close() }()
	rows, err := csv.NewReader(io.LimitReader(file, 10<<20)).ReadAll()
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid CSV")
		return
	}
	var emails []string
	for i, row := range rows {
		if len(row) == 0 {
			continue
		}
		email := strings.TrimSpace(row[0])
		if i == 0 && strings.EqualFold(email, "email") {
			continue // header row
		}
		if email != "" {
			emails = append(emails, email)
		}
	}
	imported, skipped, invalid, err := h.store.ImportSubscribers(emails)
	if err != nil {
		h.log.Error("import subscribers", "err", err)
		writeErr(w, http.StatusInternalServerError, "import failed")
		return
	}
	h.log.Info("subscribers imported", "imported", imported, "skipped", skipped, "invalid", invalid)
	h.redirect(w, r, fmt.Sprintf("/admin/subscribers?imported=%d&skipped=%d&invalid=%d",
		imported, skipped, invalid))
}

// exportSubscribers streams the list as CSV.
func (h *Handler) exportSubscribers(w http.ResponseWriter, r *http.Request) {
	subs, err := h.store.ListSubscribers("", "", 0, 1<<30)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "query failed")
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="subscribers.csv"`)
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"email", "status", "confirmed", "created_at", "confirmed_at"})
	for _, s := range subs {
		confirmed := "0"
		if s.ConfirmedAt > 0 {
			confirmed = "1"
		}
		_ = cw.Write([]string{neutralizeCell(s.Email), s.Status, confirmed,
			strconv.FormatInt(s.CreatedAt, 10), strconv.FormatInt(s.ConfirmedAt, 10)})
	}
	cw.Flush()
}

// neutralizeCell defuses CSV formula injection: RFC 5322 atext lets a local
// part start with =, +, - or @, and a spreadsheet app would execute such a
// cell as a formula on open. The leading quote marks it as text.
func neutralizeCell(v string) string {
	if v == "" {
		return v
	}
	switch v[0] {
	case '=', '+', '-', '@', '\t', '\r':
		return "'" + v
	}
	return v
}

// deleteSubscriber removes one row by id.
func (h *Handler) deleteSubscriber(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad id")
		return
	}
	if err := h.store.DeleteSubscriber(id); err != nil {
		writeErr(w, http.StatusBadRequest, "delete failed")
		return
	}
	h.log.Info("subscriber deleted", "id", id)
	h.redirect(w, r, "/admin/subscribers")
}

// commentsPage lists comments by status (default pending).
func (h *Handler) commentsPage(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	if status == "" {
		status = "pending"
	}
	comments, err := h.store.ListComments(status)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "query failed")
		return
	}
	counts := map[string]int{}
	for _, st := range []string{"pending", "approved", "rejected"} {
		if n, err := h.store.CountComments(st); err == nil {
			counts[st] = n
		}
	}
	h.render(w, "comments.html", map[string]any{
		"Title": "评论审核", "Comments": comments, "Status": status, "Counts": counts,
	})
}

// moderate handles POST approve/reject.
func (h *Handler) moderate(status string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "bad id")
			return
		}
		if err := h.store.ModerateComment(id, status); err != nil {
			writeErr(w, http.StatusBadRequest, "moderation failed")
			return
		}
		h.log.Info("comment moderated via admin", "id", id, "status", status)
		h.redirect(w, r, "/admin/comments")
	}
}

// statsJSON is the machine-readable stats endpoint.
func (h *Handler) statsJSON(w http.ResponseWriter, r *http.Request) {
	since := time.Now().Add(-30 * 24 * time.Hour).Unix()
	pv, uv, err := h.store.PageviewStats(since)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "query failed")
		return
	}
	downloads, err := h.store.DownloadStats(since)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "query failed")
		return
	}
	active, err := h.store.CountSubscribers("active", "")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "query failed")
		return
	}
	pending, err := h.store.CountComments("pending")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "query failed")
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"pv_30d":             pv,
		"uv_30d":             uv,
		"active_subscribers": active,
		"pending_comments":   pending,
		"downloads_30d":      downloads,
	})
}

// statsPage renders the same numbers as a table.
func (h *Handler) statsPage(w http.ResponseWriter, r *http.Request) {
	days := 30
	switch r.URL.Query().Get("days") {
	case "7":
		days = 7
	case "90":
		days = 90
	}
	now := time.Now()
	since := now.Add(-time.Duration(days) * 24 * time.Hour)
	pv, uv, err := h.store.PageviewStats(since.Unix())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "query failed")
		return
	}
	downloadsMap, err := h.store.DownloadStats(since.Unix())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "query failed")
		return
	}
	downloads := sortedDownloadRows(downloadsMap)
	active, err := h.store.CountSubscribers("active", "")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "query failed")
		return
	}
	pending, err := h.store.CountComments("pending")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "query failed")
		return
	}

	// ── 时间线（补零成连续序列）＋ 路径/来源维度 ──
	sparse, err := h.store.PageviewTimeseries(since.Unix())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "query failed")
		return
	}
	byDay := map[string]store.DayStat{}
	maxPV := 0
	for _, d := range sparse {
		byDay[d.Day] = d
		if d.PV > maxPV {
			maxPV = d.PV
		}
	}
	cols := make([]DayCol, 0, days+1)
	labelEvery := days / 8
	if labelEvery < 1 {
		labelEvery = 1
	}
	for i := 0; i <= days; i++ {
		day := since.AddDate(0, 0, i)
		key := day.Format("2006-01-02")
		d := byDay[key]
		pvh, uvh := 0, 0
		if maxPV > 0 {
			pvh = d.PV * 100 / maxPV
			uvh = d.UV * 100 / maxPV
			if d.PV > 0 && pvh < 2 {
				pvh = 2
			}
			if d.UV > 0 && uvh < 2 {
				uvh = 2
			}
		}
		label := ""
		if i%labelEvery == 0 || i == days {
			label = day.Format("01-02")
		}
		cols = append(cols, DayCol{Day: key, Label: label, PV: d.PV, UV: d.UV, PVH: pvh, UVH: uvh})
	}

	pathsRaw, err := h.store.PathStats(since.Unix(), 100)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "query failed")
		return
	}
	paths := make([]PathRow, 0, len(pathsRaw))
	for _, p := range pathsRaw {
		share := 0
		if pv > 0 {
			share = p.PV * 100 / pv
		}
		paths = append(paths, PathRow{PathStat: p, Share: share, Width: share})
	}

	refsRaw, err := h.store.ReferrerStats(since.Unix(), 20)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "query failed")
		return
	}
	refs := make([]RefRow, 0, len(refsRaw))
	for _, rf := range refsRaw {
		share := 0
		if pv > 0 {
			share = rf.PV * 100 / pv
		}
		display := rf.Referrer
		if display == "" {
			display = "直接访问"
		}
		refs = append(refs, RefRow{RefStat: rf, Display: display, Share: share, Width: share})
	}

	h.render(w, "stats.html", map[string]any{
		"Title": "统计", "PV30": pv, "UV30": uv, "Days": days,
		"ActiveSubs": active, "PendingComments": pending, "Downloads": downloads,
		"Timeline": cols, "Paths": paths, "Referrers": refs,
	})
}

// DayCol 是时间线的一列：日期、PV/UV 及其条高百分比、稀疏 x 轴标签。
type DayCol struct {
	Day, Label string
	PV, UV     int
	PVH, UVH   int
}

// PathRow / RefRow 是带占比与条宽的展示行。
type PathRow struct {
	store.PathStat
	Share, Width int
}

type RefRow struct {
	store.RefStat
	Display      string
	Share, Width int
}

// DownloadRow 是仪表盘条形图的一行：slug、近 30 天次数、条宽百分比（模板不做算术）。
type DownloadRow struct {
	Slug  string
	Count int
	Width int
}

// sortedDownloadRows 把下载量 map 变成降序、带条宽百分比的行。
func sortedDownloadRows(downloads map[string]int) []DownloadRow {
	rows := make([]DownloadRow, 0, len(downloads))
	maxN := 0
	for _, n := range downloads {
		if n > maxN {
			maxN = n
		}
	}
	for slug, n := range downloads {
		w := 100
		if maxN > 0 {
			w = n * 100 / maxN
		}
		if w < 3 && n > 0 {
			w = 3
		}
		rows = append(rows, DownloadRow{Slug: slug, Count: n, Width: w})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Count > rows[j].Count })
	return rows
}
