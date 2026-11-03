package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 统计页是流量分析台：时间线、路径维度、来源维度，可按时间窗筛选——
// 让人理解"谁在怎么读这个站"。
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
		`data-days="1"`, `data-days="7"`, `data-days="30"`, `data-days="90"`, // 时间窗快速筛选
		"data-day=",         // 时间线列
		"tl-num",            // 时间线柱子标数字，不是光秃秃的 bar
		"/blog/rocket.html", // 路径维度
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
	if strings.Contains(body, "TOP 100") {
		t.Error("热门路径应收敛为 TOP 20")
	}
}

// 24 小时窗口：时间线切到小时粒度，热门路径/来源跟随同一窗口——
// "刚才谁在读什么"要一眼可见。
func TestStatsPage24hQuickRange(t *testing.T) {
	fx := newAdminFixture(t)
	if err := fx.store.RecordPageview("/blog/fresh.html", "https://x.com", "ip1", "ua"); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	req := authed(http.MethodGet, "/admin/stats/html?days=1", nil, login(t, fx.handler))
	fx.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()

	for _, want := range []string{
		`class="on" data-days="1"`, // 24 小时 chip 处于选中态
		"时",                        // 小时粒度 x 轴标签
		"/blog/fresh.html",
		"x.com",
		"24 小时", // 概要卡片的窗口文案
	} {
		if !strings.Contains(body, want) {
			t.Errorf("24h 统计页缺 %q", want)
		}
	}
}
