package admin

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/iamshaynez/xiaowenz-blog-core/internal/backup"
	"github.com/iamshaynez/xiaowenz-blog-core/internal/s3store"
	"github.com/iamshaynez/xiaowenz-blog-core/internal/sysconfig"
)

func getBackupPage(t *testing.T, h http.Handler, session *http.Cookie) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/admin/backup", nil)
	req.AddCookie(session)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /admin/backup status = %d, want 200", rec.Code)
	}
	return rec.Body.String()
}

func TestBackupPageRendersCronAndHistory(t *testing.T) {
	fx := newAdminFixture(t)
	session := login(t, fx.handler)

	body := getBackupPage(t, fx.handler, session)
	if !strings.Contains(body, backup.DefaultCron) {
		t.Error("page missing current cron expression")
	}
	if !strings.Contains(body, "备份") {
		t.Error("page missing title")
	}
}

func TestBackupRunNowCreatesSnapshot(t *testing.T) {
	fx := newAdminFixture(t)
	seedBackupR2(t, fx)
	session := login(t, fx.handler)

	req := httptest.NewRequest(http.MethodPost, "/admin/backup/run", nil)
	req.AddCookie(session)
	rec := httptest.NewRecorder()
	fx.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("POST /admin/backup/run status = %d, want 303", rec.Code)
	}
	if loc := rec.Header().Get("Location"); !strings.HasPrefix(loc, "/admin/backup?ok=run") {
		t.Errorf("Location = %q, want ok=run flash", loc)
	}

	keys, _ := fx.s3.ListKeys(context.Background(), "backups/")
	found := false
	for _, k := range keys {
		if strings.HasPrefix(k, "backups/core-") && strings.HasSuffix(k, ".db") {
			found = true
		}
	}
	if !found {
		t.Error("manual run uploaded no snapshot")
	}

	body := getBackupPage(t, fx.handler, session)
	if !strings.Contains(body, "core-") {
		t.Error("history table missing the new snapshot")
	}
}

func TestBackupScheduleUpdateAppliesImmediately(t *testing.T) {
	fx := newAdminFixture(t)
	session := login(t, fx.handler)

	form := url.Values{"cron": {"15 3 * * *"}}
	req := httptest.NewRequest(http.MethodPost, "/admin/backup/schedule", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(session)
	rec := httptest.NewRecorder()
	fx.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("POST /admin/backup/schedule status = %d, want 303", rec.Code)
	}

	got, err := fx.store.GetSetting(backup.SettingKey)
	if err != nil || got != "15 3 * * *" {
		t.Fatalf("setting = %q err=%v, want 15 3 * * *", got, err)
	}
	if body := getBackupPage(t, fx.handler, session); !strings.Contains(body, "15 3 * * *") {
		t.Error("page does not show the updated cron")
	}
}

func TestBackupScheduleRejectsInvalidCron(t *testing.T) {
	fx := newAdminFixture(t)
	session := login(t, fx.handler)

	form := url.Values{"cron": {"99 * * * *"}}
	req := httptest.NewRequest(http.MethodPost, "/admin/backup/schedule", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(session)
	rec := httptest.NewRecorder()
	fx.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303 redirect", rec.Code)
	}
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "err=cron") {
		t.Errorf("Location = %q, want err=cron", loc)
	}
	if got, _ := fx.store.GetSetting(backup.SettingKey); got != "" {
		t.Errorf("invalid cron was saved: %q", got)
	}
}

func TestBackupTestR2Endpoint(t *testing.T) {
	fx := newAdminFixture(t)
	seedBackupR2(t, fx)
	session := login(t, fx.handler)

	req := httptest.NewRequest(http.MethodPost, "/admin/backup/test", nil)
	req.AddCookie(session)
	rec := httptest.NewRecorder()
	fx.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("POST /admin/backup/test status = %d, want 303", rec.Code)
	}
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "ok=test") {
		t.Errorf("Location = %q, want ok=test", loc)
	}
}

type r2FailingS3 struct{ s3store.Client }

func (s r2FailingS3) Put(context.Context, string, io.Reader, int64, string) (string, error) {
	return "", errors.New("403 InvalidAccessKeyId")
}

func TestBackupTestR2ReportsFailure(t *testing.T) {
	fx := newAdminFixture(t)
	failing := backup.New(fx.db, nil)
	failing.Client = func() s3store.Client {
		return r2FailingS3{Client: s3store.NewFake("https://backup.test")}
	}
	fx.raw.backups = failing
	session := login(t, fx.handler)

	req := httptest.NewRequest(http.MethodPost, "/admin/backup/test", nil)
	req.AddCookie(session)
	rec := httptest.NewRecorder()
	fx.handler.ServeHTTP(rec, req)
	loc := rec.Header().Get("Location")
	if !strings.Contains(loc, "err=test") {
		t.Fatalf("Location = %q, want err=test", loc)
	}
	// The flash (with the failure detail) lives in the redirect query.
	req2 := httptest.NewRequest(http.MethodGet, loc, nil)
	req2.AddCookie(session)
	rec2 := httptest.NewRecorder()
	fx.handler.ServeHTTP(rec2, req2)
	if !strings.Contains(rec2.Body.String(), "403") {
		t.Error("page missing failure detail")
	}
}

func TestBackupEndpointsRequireSession(t *testing.T) {
	fx := newAdminFixture(t)
	for _, path := range []string{"/admin/backup/run", "/admin/backup/schedule", "/admin/backup/test"} {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		rec := httptest.NewRecorder()
		fx.handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "/admin/login") {
			t.Errorf("POST %s without session = %d %q, want redirect to login", path, rec.Code, rec.Header().Get("Location"))
		}
	}
}

// seedBackupR2 stores a complete console R2 config so the fixture's
// settings-backed client factory turns on.
func seedBackupR2(t *testing.T, fx *adminFixture) {
	t.Helper()
	if err := sysconfig.SaveBackupR2(fx.store, sysconfig.BackupR2{
		Endpoint: "https://acc.r2.cloudflarestorage.com",
		KeyID:    "kid",
		Secret:   "sec",
		Bucket:   "core-backups",
	}); err != nil {
		t.Fatalf("seed r2 config: %v", err)
	}
}

func postBackupForm(t *testing.T, h http.Handler, session *http.Cookie, path string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(session)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestBackupR2ConfigSavedFromConsole(t *testing.T) {
	fx := newAdminFixture(t)
	session := login(t, fx.handler)

	rec := postBackupForm(t, fx.handler, session, "/admin/settings/backup", url.Values{
		"endpoint": {"https://acc.r2.cloudflarestorage.com"},
		"key_id":   {"kid"},
		"secret":   {"supersecret"},
		"bucket":   {"core-backups"},
	})
	if rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "ok=backup") {
		t.Fatalf("status=%d loc=%q, want 303 ok=config", rec.Code, rec.Header().Get("Location"))
	}

	got, err := sysconfig.LoadBackupR2(fx.store)
	if err != nil {
		t.Fatalf("LoadR2Config: %v", err)
	}
	if got.Endpoint != "https://acc.r2.cloudflarestorage.com" || got.KeyID != "kid" ||
		got.Secret != "supersecret" || got.Bucket != "core-backups" {
		t.Errorf("saved config = %+v", got)
	}

	body := getSettingsPage(t, fx.handler, session)
	if !strings.Contains(body, "core-backups") || !strings.Contains(body, "acc.r2.cloudflarestorage.com") {
		t.Error("settings page does not echo back endpoint/bucket")
	}
}

func TestBackupR2ConfigSecretNeverRendered(t *testing.T) {
	fx := newAdminFixture(t)
	session := login(t, fx.handler)
	postBackupForm(t, fx.handler, session, "/admin/settings/backup", url.Values{
		"endpoint": {"https://acc.r2.cloudflarestorage.com"},
		"key_id":   {"kid"},
		"secret":   {"supersecret-do-not-leak"},
		"bucket":   {"core-backups"},
	})

	body := getBackupPage(t, fx.handler, session)
	if strings.Contains(body, "supersecret-do-not-leak") {
		t.Error("page renders the raw secret")
	}
	if !strings.Contains(body, "已配置") {
		t.Error("page missing secret-configured indicator")
	}
}

func TestBackupR2ConfigBlankSecretKeepsExisting(t *testing.T) {
	fx := newAdminFixture(t)
	session := login(t, fx.handler)
	postBackupForm(t, fx.handler, session, "/admin/settings/backup", url.Values{
		"endpoint": {"https://old.r2.cloudflarestorage.com"},
		"key_id":   {"kid"},
		"secret":   {"keepme"},
		"bucket":   {"core-backups"},
	})

	// Rotate endpoint but leave the secret field blank.
	rec := postBackupForm(t, fx.handler, session, "/admin/settings/backup", url.Values{
		"endpoint": {"https://new.r2.cloudflarestorage.com"},
		"key_id":   {"kid"},
		"bucket":   {"core-backups"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	got, _ := sysconfig.LoadBackupR2(fx.store)
	if got.Secret != "keepme" {
		t.Errorf("secret = %q, want preserved", got.Secret)
	}
	if got.Endpoint != "https://new.r2.cloudflarestorage.com" {
		t.Errorf("endpoint = %q, want updated", got.Endpoint)
	}
}

func TestBackupR2ConfigRejectsBadValues(t *testing.T) {
	fx := newAdminFixture(t)
	session := login(t, fx.handler)

	for _, bad := range []url.Values{
		{"endpoint": {"ftp://acc.r2.cloudflarestorage.com"}, "key_id": {"k"}, "secret": {"s"}, "bucket": {"b"}},
		{"endpoint": {"not a url"}, "key_id": {"k"}, "secret": {"s"}, "bucket": {"b"}},
		{"endpoint": {"https://e"}, "key_id": {"k"}, "secret": {"s"}, "bucket": {"bad/bucket"}},
		{"endpoint": {"https://e"}, "key_id": {"k"}, "secret": {"s"}, "bucket": {"has space"}},
	} {
		rec := postBackupForm(t, fx.handler, session, "/admin/settings/backup", bad)
		if rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "err=r2config") {
			t.Errorf("config %v accepted: %d %q", bad, rec.Code, rec.Header().Get("Location"))
		}
	}
	// Nothing was written.
	if got, _ := sysconfig.LoadBackupR2(fx.store); got.Complete() {
		t.Errorf("invalid config persisted: %+v", got)
	}
}

func TestBackupDisabledUntilConsoleConfigured(t *testing.T) {
	fx := newAdminFixture(t)
	session := login(t, fx.handler)

	// No console config yet: manual run and connection test both refuse.
	rec := postBackupForm(t, fx.handler, session, "/admin/backup/run", nil)
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "err=unconfigured") {
		t.Fatalf("run before config: %q, want err=unconfigured", loc)
	}
	rec = postBackupForm(t, fx.handler, session, "/admin/backup/test", nil)
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "err=unconfigured") {
		t.Fatalf("test before config: %q, want err=unconfigured", loc)
	}

	// Configure in the console; the same process now backs up and tests fine.
	postBackupForm(t, fx.handler, session, "/admin/settings/backup", url.Values{
		"endpoint": {"https://acc.r2.cloudflarestorage.com"},
		"key_id":   {"kid"},
		"secret":   {"sec"},
		"bucket":   {"core-backups"},
	})
	rec = postBackupForm(t, fx.handler, session, "/admin/backup/run", nil)
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "ok=run") {
		t.Fatalf("run after config: %q, want ok=run", loc)
	}
	keys, _ := fx.s3.ListKeys(context.Background(), "backups/")
	if len(keys) == 0 {
		t.Error("no snapshot uploaded after console config")
	}
	rec = postBackupForm(t, fx.handler, session, "/admin/backup/test", nil)
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "ok=test") {
		t.Fatalf("test after config: %q, want ok=test", loc)
	}
}
