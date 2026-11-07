package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/iamshaynez/xiaowenz-blog-core/internal/sysconfig"
)

func getSettingsPage(t *testing.T, h http.Handler, session *http.Cookie) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/admin/settings", nil)
	req.AddCookie(session)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /admin/settings status = %d, want 200", rec.Code)
	}
	return rec.Body.String()
}

func TestSettingsPageShowsEnvFallbackSources(t *testing.T) {
	fx := newAdminFixture(t)
	session := login(t, fx.handler)

	body := getSettingsPage(t, fx.handler, session)
	if !strings.Contains(body, "系统参数") {
		t.Error("page missing title")
	}
	// Media fields fall back to env (fixture wires env-like values) and the
	// page must say where each effective value comes from.
	if !strings.Contains(body, "环境变量") {
		t.Error("page missing env-fallback source badge")
	}
	if !strings.Contains(body, "env-blog") {
		t.Error("page missing effective env bucket value")
	}
}

func TestSettingsMediaConfigOverridesEnv(t *testing.T) {
	fx := newAdminFixture(t)
	session := login(t, fx.handler)

	rec := postBackupForm(t, fx.handler, session, "/admin/settings/media", url.Values{
		"endpoint":    {"https://db.r2.cloudflarestorage.com"},
		"key_id":      {"db-kid"},
		"secret":      {"db-sec"},
		"bucket":      {"db-blog"},
		"public_base": {"https://db-media.test"},
	})
	if rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "ok=media") {
		t.Fatalf("status=%d loc=%q, want 303 ok=media", rec.Code, rec.Header().Get("Location"))
	}

	got, err := sysconfig.LoadMediaR2(fx.store)
	if err != nil {
		t.Fatalf("LoadMediaR2: %v", err)
	}
	if got.Bucket != "db-blog" || got.Endpoint != "https://db.r2.cloudflarestorage.com" || got.PublicBase != "https://db-media.test" {
		t.Errorf("saved media config = %+v", got)
	}

	// The page now marks the DB as the source, and the media client factory
	// (settings-backed) still resolves — uploads keep working.
	body := getSettingsPage(t, fx.handler, session)
	if !strings.Contains(body, "数据库") || strings.Contains(body, "env-blog") {
		t.Error("page should show db-sourced values after override")
	}
	if fx.mediaClient == nil || fx.mediaClient() == nil {
		t.Error("media client factory returned nil after console config")
	}
}

func TestSettingsMediaBlankSecretKeepsStored(t *testing.T) {
	fx := newAdminFixture(t)
	session := login(t, fx.handler)
	postBackupForm(t, fx.handler, session, "/admin/settings/media", url.Values{
		"endpoint":    {"https://db.r2.cloudflarestorage.com"},
		"key_id":      {"db-kid"},
		"secret":      {"db-sec"},
		"bucket":      {"db-blog"},
		"public_base": {"https://db-media.test"},
	})
	postBackupForm(t, fx.handler, session, "/admin/settings/media", url.Values{
		"bucket": {"db-blog-2"},
	})
	got, _ := sysconfig.LoadMediaR2(fx.store)
	if got.Secret != "db-sec" {
		t.Errorf("secret = %q, want preserved", got.Secret)
	}
	if got.Bucket != "db-blog-2" {
		t.Errorf("bucket = %q, want updated", got.Bucket)
	}
}

func TestSettingsSecretsNeverRendered(t *testing.T) {
	fx := newAdminFixture(t)
	session := login(t, fx.handler)
	postBackupForm(t, fx.handler, session, "/admin/settings/media", url.Values{
		"endpoint":    {"https://db.r2.cloudflarestorage.com"},
		"key_id":      {"db-kid"},
		"secret":      {"media-secret-do-not-leak"},
		"bucket":      {"db-blog"},
		"public_base": {"https://db-media.test"},
	})
	postBackupForm(t, fx.handler, session, "/admin/settings/backup", url.Values{
		"endpoint": {"https://db.r2.cloudflarestorage.com"},
		"key_id":   {"db-kid"},
		"secret":   {"backup-secret-do-not-leak"},
		"bucket":   {"core-backups"},
	})

	body := getSettingsPage(t, fx.handler, session)
	if strings.Contains(body, "media-secret-do-not-leak") || strings.Contains(body, "backup-secret-do-not-leak") {
		t.Error("page renders a raw secret")
	}
	// Env-side secret (fixture env value) must not leak either.
	if strings.Contains(body, "env-sec") {
		t.Error("page renders the env-side secret value")
	}
}

func TestSettingsMediaRejectsBadValues(t *testing.T) {
	fx := newAdminFixture(t)
	session := login(t, fx.handler)

	for _, bad := range []url.Values{
		{"endpoint": {"http://insecure.r2.cloudflarestorage.com"}},
		{"public_base": {"media.example.com"}},
		{"bucket": {"bad/bucket"}},
		{"bucket": {"has space"}},
	} {
		rec := postBackupForm(t, fx.handler, session, "/admin/settings/media", bad)
		if rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "err=r2config") {
			t.Errorf("media config %v accepted: %d %q", bad, rec.Code, rec.Header().Get("Location"))
		}
	}
	if got, _ := sysconfig.LoadMediaR2(fx.store); got != (sysconfig.MediaR2{}) {
		t.Errorf("invalid config persisted: %+v", got)
	}
}

func TestSettingsEndpointsRequireSession(t *testing.T) {
	fx := newAdminFixture(t)
	for _, path := range []string{"/admin/settings/media", "/admin/settings/backup"} {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		rec := httptest.NewRecorder()
		fx.handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "/admin/login") {
			t.Errorf("POST %s without session = %d %q, want redirect to login", path, rec.Code, rec.Header().Get("Location"))
		}
	}
}

func TestBackupPageLinksToSettings(t *testing.T) {
	fx := newAdminFixture(t)
	session := login(t, fx.handler)
	req := httptest.NewRequest(http.MethodGet, "/admin/backup", nil)
	req.AddCookie(session)
	rec := httptest.NewRecorder()
	fx.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("backup page status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "/admin/settings") {
		t.Error("backup page missing link to settings page")
	}
	_ = context.Background()
}

// ── 统计站点：多站打点域名白名单（手工维护，无环境变量） ──────────

func TestSettingsStatsSitesSavedAndShown(t *testing.T) {
	fx := newAdminFixture(t)
	session := login(t, fx.handler)

	body := getSettingsPage(t, fx.handler, session)
	if !strings.Contains(body, "统计站点") {
		t.Fatal("settings page missing 统计站点 section")
	}

	rec := postBackupForm(t, fx.handler, session, "/admin/settings/stats-sites", url.Values{
		"domains": {"https://ask.xiaowenz.com, card.xiaowenz.com\nmoney.xiaowenz.com"},
	})
	if rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "ok=stats") {
		t.Fatalf("status=%d loc=%q, want 303 ok=stats", rec.Code, rec.Header().Get("Location"))
	}

	got, err := sysconfig.LoadStatsSites(fx.store)
	if err != nil {
		t.Fatalf("LoadStatsSites: %v", err)
	}
	want := []string{"ask.xiaowenz.com", "card.xiaowenz.com", "money.xiaowenz.com"}
	if len(got) != len(want) {
		t.Fatalf("saved = %v, want %v（归一化：去 scheme、去重）", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d = %q, want %q", i, got[i], want[i])
		}
	}

	// 页面回显已保存域名。
	body = getSettingsPage(t, fx.handler, session)
	if !strings.Contains(body, "ask.xiaowenz.com") {
		t.Error("page missing saved domain")
	}
}

// 清空保存 = 撤销全部他站接入（博客不受影响）。
func TestSettingsStatsSitesCanBeCleared(t *testing.T) {
	fx := newAdminFixture(t)
	session := login(t, fx.handler)
	postBackupForm(t, fx.handler, session, "/admin/settings/stats-sites", url.Values{
		"domains": {"ask.xiaowenz.com"},
	})
	rec := postBackupForm(t, fx.handler, session, "/admin/settings/stats-sites", url.Values{
		"domains": {"  "},
	})
	if rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "ok=stats") {
		t.Fatalf("clear status=%d loc=%q", rec.Code, rec.Header().Get("Location"))
	}
	if got, _ := sysconfig.LoadStatsSites(fx.store); len(got) != 0 {
		t.Errorf("after clear = %v, want empty", got)
	}
}

// 非法条目（带路径/空格的 host）整单拒绝，不落库。
func TestSettingsStatsSitesRejectsBadValues(t *testing.T) {
	fx := newAdminFixture(t)
	session := login(t, fx.handler)
	for _, bad := range []string{"ask.xiaowenz.com/some/path", "has space.example.com", "https://ok.example.com,bad host.example.com"} {
		rec := postBackupForm(t, fx.handler, session, "/admin/settings/stats-sites", url.Values{
			"domains": {bad},
		})
		if rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "err=stats") {
			t.Errorf("domains %q accepted: %d %q", bad, rec.Code, rec.Header().Get("Location"))
		}
	}
	if got, _ := sysconfig.LoadStatsSites(fx.store); len(got) != 0 {
		t.Errorf("invalid domains persisted: %v", got)
	}
}
