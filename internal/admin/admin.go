// Package admin serves the server-rendered management console (SPEC §4).
// Authentication is an HMAC-signed session cookie; all admin POSTs check
// Origin same-site. No frontend framework — html/template only.
package admin

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/iamshaynez/xiaowenz-blog-core/internal/config"
	"github.com/iamshaynez/xiaowenz-blog-core/internal/newsletter"
	"github.com/iamshaynez/xiaowenz-blog-core/internal/ratelimit"
	"github.com/iamshaynez/xiaowenz-blog-core/internal/s3store"
	"github.com/iamshaynez/xiaowenz-blog-core/internal/store"
)

// Deps wires the admin console.
type Deps struct {
	Store  *store.Store
	Config *config.Config
	S3     s3store.Client // nil when R2 is unconfigured
	Sender *newsletter.Sender
	Logger *slog.Logger
}

// Handler is the admin console root.
type Handler struct {
	store  *store.Store
	cfg    *config.Config
	s3     s3store.Client
	sender *newsletter.Sender
	log    *slog.Logger
	secure bool

	// loginLimit throttles /admin/login to 5 attempts/min/IP.
	loginLimit *ratelimit.Limiter
	// maxUpload caps media uploads (SPEC §4: 100MB); a field so tests can shrink it.
	maxUpload int64

	// newsletter-import（SPEC §4）：从文章 URL 抓取生成草稿。
	// importHostSuffix 是 CORE_SITE_URL 派生的域名后缀（SSRF 白名单）；空 = 功能关闭。
	importHostSuffix string
	importClient     *http.Client

	// 博客文章源联动（SPEC §4）：feedURL/feedOrigin 派生自 CORE_SITE_URL；
	// posts 缓存 feedTTL，拉取失败降级为手动 URL 输入。
	feedURL    string
	feedOrigin string
	feedClient *http.Client
	mu         sync.Mutex
	posts      []feedPost
	fetchedAt  time.Time

	audioFeedURL   string
	audioPosts     []feedPost
	audioFetchedAt time.Time
}

const sessionCookieName = "core_session"
const sessionTTL = 24 * time.Hour

// NewHandler builds the admin Handler.
func NewHandler(d Deps) *Handler {
	log := d.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Handler{
		store:            d.Store,
		cfg:              d.Config,
		s3:               d.S3,
		sender:           d.Sender,
		log:              log,
		secure:           d.Config.IsProd(),
		loginLimit:       ratelimit.New(5, time.Minute),
		importHostSuffix: siteHostSuffix(d.Config.SiteURL),
		importClient:     &http.Client{Timeout: 10 * time.Second},
		feedURL:          strings.TrimRight(d.Config.SiteURL, "/") + "/feed.xml",
		audioFeedURL:     strings.TrimRight(d.Config.SiteURL, "/") + "/feed/audio.xml",
		feedOrigin:       originOf(d.Config.SiteURL),
		feedClient:       &http.Client{Timeout: 5 * time.Second},
		maxUpload:        maxUploadBytes,
	}
}

// Routes returns the admin mux with auth + origin middleware.
func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /admin/login", h.loginPage)
	mux.HandleFunc("POST /admin/login", h.login)

	// Everything below requires a valid session.
	guarded := http.NewServeMux()
	guarded.HandleFunc("GET /admin", h.dashboard)
	guarded.HandleFunc("POST /admin/logout", h.logout)
	guarded.HandleFunc("GET /admin/subscribers", h.subscribersPage)
	guarded.HandleFunc("POST /admin/subscribers/import", h.importSubscribers)
	guarded.HandleFunc("GET /admin/subscribers/export", h.exportSubscribers)
	guarded.HandleFunc("POST /admin/subscribers/{id}/delete", h.deleteSubscriber)
	guarded.HandleFunc("GET /admin/comments", h.commentsPage)
	guarded.HandleFunc("POST /admin/comments/{id}/approve", h.moderate("approved"))
	guarded.HandleFunc("POST /admin/comments/{id}/reject", h.moderate("rejected"))
	guarded.HandleFunc("GET /admin/newsletters", h.newslettersPage)
	guarded.HandleFunc("GET /admin/newsletters/new", h.newsletterNew)
	guarded.HandleFunc("POST /admin/newsletters", h.newsletterCreate)
	guarded.HandleFunc("POST /admin/newsletters/import", h.newsletterImport)
	guarded.HandleFunc("GET /admin/newsletters/{id}", h.newsletterView)
	guarded.HandleFunc("POST /admin/newsletters/{id}/send", h.newsletterSend)
	guarded.HandleFunc("POST /admin/newsletters/{id}/delete", h.newsletterDelete)
	guarded.HandleFunc("GET /admin/media", h.mediaPage)
	guarded.HandleFunc("POST /admin/media", h.mediaUpload)
	guarded.HandleFunc("GET /admin/stats", h.statsJSON)
	guarded.HandleFunc("GET /admin/stats/html", h.statsPage)

	mux.Handle("/admin", h.requireAuth(guarded))
	mux.Handle("/admin/", h.requireAuth(guarded))
	return mux
}

// requireAuth gates the guarded mux on a valid session cookie and enforces
// the Origin check on POSTs.
func (h *Handler) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && !h.sameOrigin(r) {
			h.log.Warn("admin POST rejected: cross-origin", "origin", r.Header.Get("Origin"))
			writeErr(w, http.StatusForbidden, "cross-origin request rejected")
			return
		}
		if !h.validSession(r) {
			http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// sameOrigin accepts requests with no Origin header (curl, server-side) and
// requests whose Origin host matches the request Host.
func (h *Handler) sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return u.Host == r.Host
}

// signSession produces "expiry|hmac" for the admin token.
func signSession(adminToken string, expiry int64) string {
	payload := strconv.FormatInt(expiry, 10)
	key := sha256.Sum256([]byte("core-session|" + adminToken))
	mac := hmac.New(sha256.New, key[:])
	mac.Write([]byte(payload))
	return payload + "|" + hex.EncodeToString(mac.Sum(nil))
}

// verifySession checks signature and expiry in constant time.
func verifySession(adminToken, value string, now time.Time) bool {
	parts := strings.Split(value, "|")
	if len(parts) != 2 {
		return false
	}
	expiry, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || now.Unix() > expiry {
		return false
	}
	want := signSession(adminToken, expiry)
	gotSig, err := hex.DecodeString(parts[1])
	if err != nil {
		return false
	}
	wantSig, err := hex.DecodeString(strings.Split(want, "|")[1])
	if err != nil {
		return false
	}
	return hmac.Equal(wantSig, gotSig)
}

func (h *Handler) validSession(r *http.Request) bool {
	if h.cfg.AdminToken == "" {
		return false // empty token = admin disabled (SPEC §1)
	}
	c, err := r.Cookie(sessionCookieName)
	if err != nil {
		return false
	}
	return verifySession(h.cfg.AdminToken, c.Value, time.Now())
}

// loginPage renders the password form.
func (h *Handler) loginPage(w http.ResponseWriter, r *http.Request) {
	h.render(w, "login.html", map[string]any{"Title": "登录"})
}

// login compares the token in constant time and issues the session cookie.
func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	if !h.loginLimit.Allow(adminClientIP(r)) {
		h.log.Warn("admin login rate-limited", "ip", adminClientIP(r))
		writeErr(w, http.StatusTooManyRequests, "too many login attempts")
		return
	}
	token := r.FormValue("token")
	given := []byte(token)
	want := []byte(h.cfg.AdminToken)
	if len(given) == 0 || len(want) == 0 ||
		len(given) != len(want) || subtle.ConstantTimeCompare(given, want) != 1 {
		h.log.Warn("admin login failed")
		writeErr(w, http.StatusUnauthorized, "invalid token")
		return
	}
	cookie := &http.Cookie{
		Name:     sessionCookieName,
		Value:    signSession(h.cfg.AdminToken, time.Now().Add(sessionTTL).Unix()),
		Path:     "/admin",
		HttpOnly: true,
		Secure:   h.secure,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Now().Add(sessionTTL),
	}
	http.SetCookie(w, cookie)
	h.log.Info("admin login")
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

// adminClientIP returns the peer address (admin surface never trusts XFF —
// the console is not behind a public CDN and spoofing must not bypass the
// login rate limit).
func adminClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// logout expires the session cookie client-side. The cookie is a stateless
// HMAC value, so a copied cookie stays valid until its own expiry — rotating
// CORE_ADMIN_TOKEN is the server-side kill switch.
func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/admin",
		HttpOnly: true,
		Secure:   h.secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
	h.log.Info("admin logout")
	http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(`{"error":` + strconv.Quote(msg) + `}`))
}

func (h *Handler) redirect(w http.ResponseWriter, r *http.Request, to string) {
	http.Redirect(w, r, to, http.StatusSeeOther)
}
