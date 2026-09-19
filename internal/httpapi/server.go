// Package httpapi serves the public JSON API (SPEC §3).
package httpapi

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/xiaowenz/xiaowen-core/internal/config"
	"github.com/xiaowenz/xiaowen-core/internal/mailer"
	"github.com/xiaowenz/xiaowen-core/internal/ratelimit"
	"github.com/xiaowenz/xiaowen-core/internal/store"
	"github.com/xiaowenz/xiaowen-core/internal/turnstile"
)

// Deps wires the public API server.
type Deps struct {
	Store    *store.Store
	Mailer   mailer.Mailer
	Verifier turnstile.Verifier
	Config   *config.Config
	Logger   *slog.Logger
}

// Server is the public API handler set.
type Server struct {
	store    *store.Store
	mailer   mailer.Mailer
	verifier turnstile.Verifier
	cfg      *config.Config
	log      *slog.Logger

	subLimit *ratelimit.Limiter
	comLimit *ratelimit.Limiter
	pvLimit  *ratelimit.Limiter

	signKey []byte
}

// NewServer builds the Server with SPEC rate limits (5/h subscribe, 10/h
// comments, 60/min pageviews).
func NewServer(d Deps) *Server {
	log := d.Logger
	if log == nil {
		log = slog.Default()
	}
	key := sha256.Sum256([]byte("core-moderate-sign|" + d.Config.AdminToken))
	return &Server{
		store:    d.Store,
		mailer:   d.Mailer,
		verifier: d.Verifier,
		cfg:      d.Config,
		log:      log,
		subLimit: ratelimit.New(5, time.Hour),
		comLimit: ratelimit.New(10, time.Hour),
		pvLimit:  ratelimit.New(60, time.Minute),
		signKey:  key[:],
	}
}

// Close releases limiter goroutines.
func (s *Server) Close() {
	s.subLimit.Close()
	s.comLimit.Close()
	s.pvLimit.Close()
}

// Routes returns the public mux with CORS restricted to CORE_SITE_URL.
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/subscribe", s.handleSubscribe)
	mux.HandleFunc("GET /api/verify", s.handleVerify)
	mux.HandleFunc("GET /api/unsub", s.handleUnsubPage)
	mux.HandleFunc("POST /api/unsub", s.handleUnsub)
	mux.HandleFunc("POST /api/comments", s.handleCreateComment)
	mux.HandleFunc("GET /api/comments", s.handleListComments)
	mux.HandleFunc("GET /api/comments/moderate", s.handleModerate)
	mux.HandleFunc("POST /api/pv", s.handlePageview)
	mux.HandleFunc("GET /a/", s.handleDownload)
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	return s.cors(mux)
}

// cors allows only the configured site origin and answers preflight.
func (s *Server) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", s.cfg.SiteURL)
		w.Header().Set("Vary", "Origin")
		if r.Method == http.MethodOptions {
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// maxPublicBodyBytes caps JSON bodies on public write endpoints at 1 MiB —
// the largest legitimate payload (a 2000-char comment) is a few KB.
const maxPublicBodyBytes = 1 << 20

// decodeJSONBody reads a capped JSON body; an over-cap body is answered 413,
// malformed JSON 400. Returns false when the request was already answered.
func decodeJSONBody(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxPublicBodyBytes)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			writeErr(w, http.StatusRequestEntityTooLarge, "body too large")
			return false
		}
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return false
	}
	return true
}

// clientIP resolves the caller's IP. X-Forwarded-For is honored only when
// the immediate peer is a configured trusted proxy (CORE_TRUSTED_PROXIES);
// otherwise the peer address is used, so a direct client cannot spoof its IP
// to evade rate limits or poison stats.
func (s *Server) clientIP(r *http.Request) string {
	peer := r.RemoteAddr
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		peer = host
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if addr, err := netip.ParseAddr(peer); err == nil && s.cfg.TrustsProxy(addr) {
			return strings.TrimSpace(strings.Split(xff, ",")[0])
		}
	}
	return peer
}

func (s *Server) ipHash(r *http.Request) string {
	return store.HashIP(s.clientIP(r), time.Now())
}

// guardWrite applies the write-endpoint triad: honeypot (silent 202), IP rate
// limit (429), Turnstile (403). Returns false when the request was answered.
func (s *Server) guardWrite(w http.ResponseWriter, r *http.Request, honeypot, token string, lim *ratelimit.Limiter) bool {
	if honeypot != "" {
		writeJSON(w, http.StatusAccepted, map[string]bool{"ok": true})
		return false
	}
	if !lim.Allow(s.clientIP(r)) {
		writeErr(w, http.StatusTooManyRequests, "too many requests")
		return false
	}
	ok, err := s.verifier.Verify(r.Context(), token, s.clientIP(r))
	if err != nil {
		s.log.Warn("turnstile verify error", "err", err)
		writeErr(w, http.StatusForbidden, "verification failed")
		return false
	}
	if !ok {
		writeErr(w, http.StatusForbidden, "verification failed")
		return false
	}
	return true
}

// signModerate builds the one-click moderation token: id.action.hmac.
func (s *Server) signModerate(id int64, action string) string {
	payload := strconv.FormatInt(id, 10) + "." + action
	mac := hmac.New(sha256.New, s.signKey)
	mac.Write([]byte(payload))
	return payload + "." + hex.EncodeToString(mac.Sum(nil))
}

// verifyModerate checks a moderation token in constant time.
func (s *Server) verifyModerate(token string) (id int64, action string, ok bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return 0, "", false
	}
	payload := parts[0] + "." + parts[1]
	mac := hmac.New(sha256.New, s.signKey)
	mac.Write([]byte(payload))
	want := mac.Sum(nil)
	got, err := hex.DecodeString(parts[2])
	if err != nil || !hmac.Equal(want, got) {
		return 0, "", false
	}
	id, err = strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0, "", false
	}
	if parts[1] != "approve" && parts[1] != "reject" {
		return 0, "", false
	}
	return id, parts[1], true
}

// handleHealthz reports liveness and db reachability (SPEC §3.4).
func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.store.Ping(ctx); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]bool{"ok": false, "db": false})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true, "db": true})
}
