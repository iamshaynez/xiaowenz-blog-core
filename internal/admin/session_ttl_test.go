package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// 管理台是个人低频工具：登录一次就该长期有效（至少 1 年），
// 否则每次进控制台都要重新拿 token 登录。
func TestSessionCookieLastsAtLeastOneYear(t *testing.T) {
	fx := newAdminFixture(t)

	session := login(t, fx.handler)

	// 浏览器侧：cookie 的 Expires 至少在 364 天之后（留 1 天余量）。
	if session.Expires.IsZero() {
		t.Fatal("session cookie must set an explicit Expires")
	}
	if d := time.Until(session.Expires); d < 364*24*time.Hour {
		t.Errorf("cookie 有效期 %v，不足一年", d)
	}

	// 服务端侧：HMAC 签名里的过期时间同样要撑过一年后校验。
	yearLater := time.Now().Add(364 * 24 * time.Hour)
	if !verifySession(fx.cfg.AdminToken, session.Value, yearLater) {
		t.Error("一年内会话不应失效")
	}
}

// 顺手回归：登出必须立即作废 cookie（MaxAge<0）。
func TestLogoutExpiresCookieImmediately(t *testing.T) {
	fx := newAdminFixture(t)
	session := login(t, fx.handler)

	rec := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/admin/logout", strings.NewReader(""))
	req.AddCookie(session)
	fx.handler.ServeHTTP(rec, req)

	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookieName && c.MaxAge >= 0 {
			t.Errorf("logout 后 MaxAge=%d，应为负值立即过期", c.MaxAge)
		}
	}
}
