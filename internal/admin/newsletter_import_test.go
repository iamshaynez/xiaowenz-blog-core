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
	// og:title 干净（不带站名后缀），<title> 带后缀 —— 主题必须取 og:title
	page := startPostPage(t, 200, `<!doctype html><html><head>
<title>每个人都快起来了，公司为什么没快起来？ - 三杯过后 | Xiaowen.Z Deployed</title>
<meta property="og:title" content="每个人都快起来了，公司为什么没快起来？">
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

// 全文导入：页面带玄墨主题正文容器（div.article__body …）时，草稿正文
// = 清洗后的全文 HTML（URL 绝对化、去 schema 噪音、注入 inline style），
// post_url 记录来源；页眉页脚/目录不进正文。
func TestNewsletterImportFullArticle(t *testing.T) {
	fx := newAdminFixture(t)
	page := startPostPage(t, 200, `<!doctype html><html><head>
<title>每个人都快起来了 - 三杯过后</title>
<meta property="og:title" content="每个人都快起来了">
<meta property="og:description" content="AI 提效从来就不是一个个人效率问题">
</head><body>
<article class="container article h-entry" itemscope itemtype="http://schema.org/Article">
  <header class="article__header"><h1 class="article__title">每个人都快起来了</h1></header>
  <aside class="toc"><div class="toc__title">目录</div><ul><li><a href="#a">一</a></li></ul></aside>
  <div class="article__body prose e-content js-content" itemprop="articleBody">
    <h2>组织效率</h2>
    <p>正文第一段，见 <a href="/about/">关于我</a>。</p>
    <p><img src="img/pic.png" alt="配图"></p>
    <div itemprop="image" itemscope itemtype="https://schema.org/ImageObject">
      <meta itemprop="url" content="https://x/icon.jpg">
    </div>
    <meta itemprop="dateModified" content="2026-11-11T00:00:00Z"/>
  </div>
  <footer class="article__footer"><a href="/blog/">← 全部文章</a></footer>
</article>
</body></html>`)
	allowImportsForTest(t, fx.raw, page.URL)
	postURL := page.URL + "/blog/ai-performance.html"

	rec := postForm(fx.handler, login(t, fx.handler), "/admin/newsletters/import",
		"url="+url.QueryEscape(postURL))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body=%s", rec.Code, rec.Body)
	}

	list, _ := fx.store.ListNewsletters()
	if len(list) != 1 {
		t.Fatalf("newsletters = %d, want 1", len(list))
	}
	n := list[0]
	if n.Subject != "每个人都快起来了" {
		t.Errorf("subject = %q", n.Subject)
	}
	if n.PostURL != postURL {
		t.Errorf("post_url = %q, want %q", n.PostURL, postURL)
	}
	// 全文进正文（h2 已注入 inline style；首段被首字下沉 span 拆开，分段断言）。
	if !strings.Contains(n.HTML, ">组织效率</h2>") || !strings.Contains(n.HTML, "文第一段") {
		t.Errorf("全文未进正文: %q", n.HTML)
	}
	// 相对 URL 绝对化（链接按站点根、图片按文章目录）。
	if !strings.Contains(n.HTML, `href="`+page.URL+`/about/"`) {
		t.Errorf("链接未绝对化: %q", n.HTML)
	}
	if !strings.Contains(n.HTML, `src="`+page.URL+`/blog/img/pic.png"`) {
		t.Errorf("图片未按文章目录绝对化: %q", n.HTML)
	}
	// schema 噪音与主题页脚/目录不进正文。
	for _, noise := range []string{"itemprop", "dateModified", "全部文章", "toc__title"} {
		if strings.Contains(n.HTML, noise) {
			t.Errorf("正文混入 %q: %q", noise, n.HTML)
		}
	}
	// 邮件化 inline style 已注入。
	if !strings.Contains(n.HTML, "<p style=") {
		t.Errorf("inline style 未注入: %q", n.HTML)
	}
	// markdown 列存来源占位（编辑视角可见导入地址）。
	if !strings.Contains(n.Markdown, postURL) {
		t.Errorf("markdown 缺来源地址: %q", n.Markdown)
	}
}

// 页面没有正文容器时回退摘要模式（og:description + 阅读全文链接），
// post_url 仍记录来源。
func TestNewsletterImportFallsBackToSummary(t *testing.T) {
	fx := newAdminFixture(t)
	page := startPostPage(t, 200, `<!doctype html><html><head>
<title>旧版文章</title>
</head><body><article>纯正文，无主题容器</article></body></html>`)
	allowImportsForTest(t, fx.raw, page.URL)
	postURL := page.URL + "/blog/legacy.html"

	rec := postForm(fx.handler, login(t, fx.handler), "/admin/newsletters/import",
		"url="+url.QueryEscape(postURL))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body)
	}
	list, _ := fx.store.ListNewsletters()
	if len(list) != 1 {
		t.Fatalf("newsletters = %d", len(list))
	}
	n := list[0]
	if n.PostURL != postURL {
		t.Errorf("post_url = %q, want %q", n.PostURL, postURL)
	}
	if !strings.Contains(n.Markdown, "阅读全文") || !strings.Contains(n.Markdown, postURL) {
		t.Errorf("摘要模式回退失败: %q", n.Markdown)
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
