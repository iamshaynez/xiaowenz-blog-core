package admin

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// startPostPage serves a fake blog post page the importer can fetch.
func startPostPage(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// allowImportsForTest points the handler's host allowlist at the test server.
func allowImportsForTest(t *testing.T, h *Handler, rawurl string) {
	t.Helper()
	u, _ := url.Parse(rawurl)
	h.importHostSuffix = u.Hostname()
}

func postForm(h http.Handler, session *http.Cookie, path, form string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(session)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// 从博客文章 URL 一键生成 Newsletter 草稿：抓取页面，标题进主题、
// og:description 进导语、原文链接进正文，落库为 draft（绝不发送）。
func TestNewsletterImportFromPost(t *testing.T) {
	fx := newAdminFixture(t)
	page := startPostPage(t, 200, `<!doctype html><html><head>
<title>每个人都快起来了，公司为什么没快起来？</title>
<meta property="og:description" content="AI 提效从来就不是一个个人效率问题">
</head><body><article>正文</article></body></html>`)
	allowImportsForTest(t, fx.raw, page.URL)

	rec := postForm(fx.handler, login(t, fx.handler), "/admin/newsletters/import",
		"url="+url.QueryEscape(page.URL+"/blog/ai-performance.html"))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body=%s", rec.Code, rec.Body)
	}
	loc := rec.Header().Get("Location")
	if !strings.HasPrefix(loc, "/admin/newsletters/") {
		t.Fatalf("Location = %q, want draft view", loc)
	}

	list, err := fx.store.ListNewsletters()
	if err != nil || len(list) != 1 {
		t.Fatalf("newsletters = %v, %v", list, err)
	}
	n := list[0]
	if n.Status != "draft" {
		t.Errorf("status = %q, want draft", n.Status)
	}
	if n.Subject != "每个人都快起来了，公司为什么没快起来？" {
		t.Errorf("subject = %q", n.Subject)
	}
	if !strings.Contains(n.Markdown, "AI 提效从来就不是一个个人效率问题") {
		t.Errorf("markdown 缺导语: %q", n.Markdown)
	}
	if !strings.Contains(n.Markdown, page.URL+"/blog/ai-performance.html") {
		t.Errorf("markdown 缺原文链接: %q", n.Markdown)
	}
	if !strings.Contains(n.HTML, "<h2>") && !strings.Contains(n.HTML, "<p>") {
		t.Errorf("html 未渲染 markdown: %q", n.HTML)
	}
}

// 站外 URL 一律拒绝（SSRF 防线），且不落任何草稿。
func TestNewsletterImportRejectsForeignHost(t *testing.T) {
	fx := newAdminFixture(t)
	page := startPostPage(t, 200, "<title>x</title>")
	allowImportsForTest(t, fx.raw, page.URL)

	rec := postForm(fx.handler, login(t, fx.handler), "/admin/newsletters/import",
		"url=https%3A%2F%2Fevil.example.com%2Fblog%2Fx.html")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	list, _ := fx.store.ListNewsletters()
	if len(list) != 0 {
		t.Errorf("foreign import must not create a draft, got %d", len(list))
	}
}

// 白名单内的 URL 抓取失败（非 200 / 超时 / 坏地址）→ 报错且不建草稿。
func TestNewsletterImportFetchFailure(t *testing.T) {
	fx := newAdminFixture(t)
	page := startPostPage(t, 404, "not found")
	allowImportsForTest(t, fx.raw, page.URL)

	for i, raw := range []string{
		page.URL + "/blog/missing.html", // 404
		"not a url",                     // 解析失败
	} {
		rec := postForm(fx.handler, login(t, fx.handler), "/admin/newsletters/import",
			"url="+url.QueryEscape(raw))
		if rec.Code < 400 {
			t.Errorf("case %d: status = %d, want 4xx", i, rec.Code)
		}
	}
	list, _ := fx.store.ListNewsletters()
	if len(list) != 0 {
		t.Errorf("failed import must not create a draft, got %d", len(list))
	}
}
