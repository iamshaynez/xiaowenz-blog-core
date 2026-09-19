package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestImportSurfacesCounts(t *testing.T) {
	fx := newAdminFixture(t)
	session := login(t, fx.handler)

	csv := "email,confirmed\nok@x.com,1\ndup@x.com,1\ndup@x.com,1\nbad-row,\n"
	body, ct := makeMultipart(t, "file", "subs.csv", "text/csv", []byte(csv))
	req := authed(http.MethodPost, "/admin/subscribers/import", body, session)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	fx.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("import: %d", rec.Code)
	}
	loc := rec.Header().Get("Location")
	for _, want := range []string{"imported=2", "skipped=1", "invalid=1"} {
		if !strings.Contains(loc, want) {
			t.Errorf("redirect %q missing %q", loc, want)
		}
	}

	// The subscribers page renders the import outcome notice.
	rec = httptest.NewRecorder()
	fx.handler.ServeHTTP(rec, authed(http.MethodGet, "/admin"+strings.TrimPrefix(loc, "/admin"), nil, session))
	if rec.Code != http.StatusOK {
		t.Fatalf("subscribers page: %d", rec.Code)
	}
	page := rec.Body.String()
	if !strings.Contains(page, "2") || !strings.Contains(page, "无效") && !strings.Contains(page, "invalid") {
		t.Errorf("import outcome notice missing from page")
	}
}
