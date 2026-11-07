package httpapi

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/iamshaynez/xiaowenz-blog-core/internal/sysconfig"
)

// handlePageview records a pageview unless DNT is set (SPEC §3.3).
//
// 多站打点门禁：站点身份取自 Origin 头（浏览器跨域 POST 必带，不可客户端
// 伪造）——CORE_SITE_URL 本域记为 site=”（博客存量连续），后台「系统参数」
// 注册的域名（sysconfig stats_sites）记为其 host，其余（含无 Origin 的非
// 浏览器请求）一律 204 静默丢弃。sendBeacon 的 text/plain 文本体无需 CORS
// 预检，天然兼容。
func (s *Server) handlePageview(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("DNT") == "1" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	// 限流在门禁之前：未知来源的轰炸同样吃 429，不给免费额度。
	if !s.pvLimit.Allow(s.clientIP(r)) {
		writeErr(w, http.StatusTooManyRequests, "too many requests")
		return
	}
	site, ok := s.beaconSite(r)
	if !ok {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	var req struct {
		Path     string `json:"path"`
		Referrer string `json:"referrer"`
	}
	if !decodeJSONBody(w, r, &req) {
		return
	}
	req.Path = strings.TrimSpace(req.Path)
	if req.Path == "" {
		writeErr(w, http.StatusBadRequest, "path required")
		return
	}
	if err := s.store.RecordPageview(site, req.Path, req.Referrer, s.ipHash(r), r.UserAgent()); err != nil {
		s.log.Error("record pageview", "err", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// beaconSite resolves the stats site for a beacon request: the site URL host
// maps to "" (blog continuity), console-registered domains map to their host,
// anything else is rejected. Missing Origin never passes — browsers always
// send it on cross-origin POSTs, so only non-browser clients lose it.
func (s *Server) beaconSite(r *http.Request) (string, bool) {
	host := originHost(r.Header.Get("Origin"))
	if host == "" {
		return "", false
	}
	if host == s.siteHost {
		return "", true
	}
	// 读名单失败按空名单处理（fail closed）：博客不受影响，他站当天丢点。
	domains, err := sysconfig.LoadStatsSites(s.store)
	if err != nil {
		s.log.Error("load stats sites", "err", err)
		return "", false
	}
	for _, d := range domains {
		if d == host {
			return host, true
		}
	}
	return "", false
}

// originHost extracts the lowercase host from an Origin header value
// (scheme://host[:port]); "null" and malformed values yield "".
func originHost(origin string) string {
	if origin == "" || strings.EqualFold(origin, "null") {
		return ""
	}
	u, err := url.Parse(origin)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}
