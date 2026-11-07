package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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
		if err := fx.store.RecordPageview("", x.path, x.ref, x.ip, "ua"); err != nil {
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
	if err := fx.store.RecordPageview("", "/blog/fresh.html", "https://x.com", "ip1", "ua"); err != nil {
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

// 多站总览：统计页列出各接入站的今日/近7天/近30天 PV/UV，博客在列。
func TestStatsPageMultiSiteOverview(t *testing.T) {
	fx := newAdminFixture(t)
	fx.store.PVRetention = 30 * 24 * time.Hour
	// 博客今天一条、ask 站今天两条。
	if err := fx.store.RecordPageview("", "/blog/a", "", "h1", "ua"); err != nil {
		t.Fatal(err)
	}
	for _, ip := range []string{"a1", "a2"} {
		if err := fx.store.RecordPageview("ask.xiaowenz.com", "/q", "", ip, "ua"); err != nil {
			t.Fatal(err)
		}
	}

	rec := httptest.NewRecorder()
	req := authed(http.MethodGet, "/admin/stats/html?days=30", nil, login(t, fx.handler))
	fx.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"ask.xiaowenz.com", "多站"} {
		if !strings.Contains(body, want) {
			t.Errorf("统计页缺 %q", want)
		}
	}
}

// ── 按站过滤：?site= 让整页（总量/时间线/路径/来源）切到该站口径 ──────

// 过滤视图只看该站：ask 的路径出现、博客路径不混入；剧集下载（博客专属）
// 隐藏；窗口 chips 保留 site 参数（切窗口不丢口径）。
func TestStatsPageSiteFilter(t *testing.T) {
	fx := newAdminFixture(t)
	fx.store.PVRetention = 30 * 24 * time.Hour
	if err := fx.store.RecordPageview("", "/blog/only.html", "", "h1", "ua"); err != nil {
		t.Fatal(err)
	}
	if err := fx.store.RecordPageview("ask.xiaowenz.com", "/ask/only.html", "", "a1", "ua"); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	req := authed(http.MethodGet, "/admin/stats/html?days=30&site=ask.xiaowenz.com", nil, login(t, fx.handler))
	fx.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()

	if !strings.Contains(body, "/ask/only.html") {
		t.Error("过滤视图缺 ask 站路径")
	}
	if strings.Contains(body, "/blog/only.html") {
		t.Error("过滤视图混入了博客路径")
	}
	if !strings.Contains(body, `href="/admin/stats/html?days=7&site=ask.xiaowenz.com"`) {
		t.Error("窗口 chips 未保留 site 参数")
	}
	if strings.Contains(body, "剧集下载量") {
		t.Error("他站视图不应显示剧集下载卡")
	}
}

// 默认视图：多站总览行可点进该站过滤视图（他站带 site=，本站回到默认）。
func TestStatsPageSiteSwitchLinks(t *testing.T) {
	fx := newAdminFixture(t)
	fx.store.PVRetention = 30 * 24 * time.Hour
	if err := fx.store.RecordPageview("ask.xiaowenz.com", "/q", "", "a1", "ua"); err != nil {
		t.Fatal(err)
	}
	if err := fx.store.RecordPageview("", "/blog/a", "", "h1", "ua"); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	req := authed(http.MethodGet, "/admin/stats/html?days=7", nil, login(t, fx.handler))
	fx.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `href="/admin/stats/html?days=7&site=ask.xiaowenz.com"`) {
		t.Error("多站总览 ask 行缺少进入该站视图的链接")
	}
	// 本站行回到默认口径：存在不带 site 的总览链接（days=7 的裸链接）。
	if !strings.Contains(body, `href="/admin/stats/html?days=7"`) {
		t.Error("多站总览本站行缺少回到默认视图的链接")
	}
}

// 未知 site 值渲染空数据页，不 500。
func TestStatsPageUnknownSiteEmpty(t *testing.T) {
	fx := newAdminFixture(t)
	rec := httptest.NewRecorder()
	req := authed(http.MethodGet, "/admin/stats/html?days=7&site=nope.example.com", nil, login(t, fx.handler))
	fx.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}
