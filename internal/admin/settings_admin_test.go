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
