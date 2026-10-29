package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 统计页是流量分析台：时间线、路径维度、来源维度，可按时间窗筛选、
// 可按路径检索——让人理解"谁在怎么读这个站"。
func TestStatsPageRichAnalytics(t *testing.T) {
	fx := newAdminFixture(t)
	for _, x := range []struct{ path, ref, ip string }{
		{"/blog/rocket.html", "https://google.com", "ip1"},
		{"/blog/rocket.html", "https://google.com", "ip2"},
		{"/blog/rocket.html", "", "ip1"},
		{"/episodes/ep001", "", "ip3"},
	} {
		if err := fx.store.RecordPageview(x.path, x.ref, x.ip, "ua"); err != nil {
			t.Fatal(err)
		}
	}

	rec := httptest.NewRecorder()
	req := authed(http.MethodGet, "/admin/stats/html?days=7", nil, login(t, fx.handler))
	fx.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()

	for _, want := range []string{
		`data-days="7"`, `data-days="30"`, `data-days="90"`, // 时间窗筛选
		"data-day=",         // 时间线列
		"/blog/rocket.html", // 路径维度
		"ip-path-filter",    // 路径检索输入
		"google.com",        // 来源维度
		"直接访问",              // 空 referrer 的展示名
	} {
		if !strings.Contains(body, want) {
			t.Errorf("统计页缺 %q", want)
		}
	}
	// 火箭文的 PV=3 应高于剧集页 PV=1（排序）
	if ir, ie := strings.Index(body, "/blog/rocket.html"), strings.Index(body, "/episodes/ep001"); ir > ie {
		t.Error("路径维度未按 PV 降序")
	}
}
