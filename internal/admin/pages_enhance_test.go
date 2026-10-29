package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ── 仪表盘 ────────────────────────────────────────────────────────────

// 下载量按大小降序渲染（图表条形从长到短），并在页面直接给出最近待审评论，
// 让仪表盘成为"打开就知道该干什么"的操作台。
func TestDashboardSortedDownloadsAndPendingPreview(t *testing.T) {
	fx := newAdminFixture(t)
	recordDownload(t, fx, "ep-b", 2)
	recordDownload(t, fx, "ep-a", 7)
	if _, err := fx.store.CreateComment("post-x", "读者", "", "这篇写得真好", "ip", "ua"); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	req := authed(http.MethodGet, "/admin", nil, login(t, fx.handler))
	fx.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "data-slug=\"ep-a\"") || !strings.Contains(body, "data-slug=\"ep-b\"") {
		t.Error("下载图表行缺 data-slug（条形渲染锚点）")
	}
	if ia, ib := strings.Index(body, "ep-a"), strings.Index(body, "ep-b"); ia > ib {
		t.Errorf("下载量未按降序：ep-a 位置 %d 应先于 ep-b %d", ia, ib)
	}
	if !strings.Contains(body, "这篇写得真好") {
		t.Error("仪表盘缺最近待审评论预览")
	}
	if !strings.Contains(body, "/admin/comments/post-x") && !strings.Contains(body, "post-x") {
		t.Error("待审预览应关联到文章 slug")
	}
}

// ── 评论页：状态页签带计数 ────────────────────────────────────────────

func TestCommentsPageStatusCounts(t *testing.T) {
	fx := newAdminFixture(t)
	for i := 0; i < 2; i++ {
		if _, err := fx.store.CreateComment("p", "n", "", "c", "ip", "ua"); err != nil {
			t.Fatal(err)
		}
	}
	rec := httptest.NewRecorder()
	req := authed(http.MethodGet, "/admin/comments", nil, login(t, fx.handler))
	fx.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{`data-count-status="pending">2`, `data-count-status="approved">0`, `data-count-status="rejected">0`} {
		if !strings.Contains(body, want) {
			t.Errorf("状态页签缺计数 %q", want)
		}
	}
}

// ── 订阅页：状态页签带计数 ────────────────────────────────────────────

func TestSubscribersPageStatusCounts(t *testing.T) {
	fx := newAdminFixture(t)
	insertSubscriber(t, fx, "a@x.com", "active")
	insertSubscriber(t, fx, "b@x.com", "active")
	insertSubscriber(t, fx, "c@x.com", "pending")

	rec := httptest.NewRecorder()
	req := authed(http.MethodGet, "/admin/subscribers", nil, login(t, fx.handler))
	fx.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{`data-count-status="active">2`, `data-count-status="pending">1`, `data-count-status="unsubscribed">0`} {
		if !strings.Contains(body, want) {
			t.Errorf("状态页签缺计数 %q", want)
		}
	}
}

// ── Newsletter 发送确认：发送前知道要发给多少人 ──────────────────────

func TestNewsletterViewShowsRecipientCount(t *testing.T) {
	fx := newAdminFixture(t)
	insertSubscriber(t, fx, "a@x.com", "active")
	insertSubscriber(t, fx, "b@x.com", "active")
	insertSubscriber(t, fx, "c@x.com", "unsubscribed")
	id, err := fx.store.CreateNewsletter("测试刊", "**hi**", "<p>hi</p>")
	if err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	req := authed(http.MethodGet, "/admin/newsletters/1", nil, login(t, fx.handler))
	fx.handler.ServeHTTP(rec, req)
	_ = id
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "2 位订阅者") {
		t.Errorf("发送确认应显示 active 订阅者数（2 位），页面未见。含 325？%v", strings.Contains(body, "325"))
	}
	if !strings.Contains(body, "confirm-send") {
		t.Error("发送按钮应带 confirm-send 确认交互")
	}
}

// ── 测试助手 ─────────────────────────────────────────────────────────

func recordDownload(t *testing.T, fx *adminFixture, slug string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if _, err := fx.store.RecordDownloadDedup(slug, "ip-"+slug+"-"+strings.Repeat("x", i+1), "ua", ""); err != nil {
			t.Fatal(err)
		}
	}
}

func insertSubscriber(t *testing.T, fx *adminFixture, email, status string) {
	t.Helper()
	sub, err := fx.store.UpsertPending(email, "tok-"+email, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if status == "active" {
		if err := fx.store.Activate(sub.ID); err != nil {
			t.Fatal(err)
		}
	} else if status == "unsubscribed" {
		if err := fx.store.UnsubscribeByToken(sub.UnsubToken); err != nil {
			t.Fatal(err)
		}
	}
}

// ── Newsletter 删除：草稿/已发可删，在途拒删 ─────────────────────────
func TestNewsletterDelete(t *testing.T) {
	fx := newAdminFixture(t)
	if _, err := fx.store.CreateNewsletter("测试刊", "md", "<p>md</p>"); err != nil {
		t.Fatal(err)
	}
	s := login(t, fx.handler)

	rec := postForm(fx.handler, s, "/admin/newsletters/1/delete", "")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body=%s", rec.Code, rec.Body)
	}
	if list, _ := fx.store.ListNewsletters(); len(list) != 0 {
		t.Errorf("删除后列表应空")
	}

	// sending 在途拒删 → 409
	id2, err := fx.store.CreateNewsletter("发送中", "md", "<p>x</p>")
	if err != nil {
		t.Fatal(err)
	}
	if err := fx.store.MarkNewsletterSending(id2); err != nil {
		t.Fatal(err)
	}
	rec2 := postForm(fx.handler, s, "/admin/newsletters/2/delete", "")
	if rec2.Code != http.StatusConflict {
		t.Errorf("sending 拒删 status = %d, want 409", rec2.Code)
	}

	// 列表页有删除按钮（带确认），sending 状态不出
	_, _ = fx.store.CreateNewsletter("带按钮的草稿", "md", "<p>x</p>")
	page := httptest.NewRecorder()
	req := authed(http.MethodGet, "/admin/newsletters", nil, s)
	fx.handler.ServeHTTP(page, req)
	body := page.Body.String()
	if !strings.Contains(body, "/admin/newsletters/3/delete") {
		t.Error("列表页缺删除入口")
	}
	if !strings.Contains(body, "confirm-del") {
		t.Error("删除按钮应带确认交互")
	}
}
