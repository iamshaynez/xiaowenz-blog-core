package httpapi

import (
	"net/http"
	"strings"
)

// handlePageview records a pageview unless DNT is set (SPEC §3.3).
func (s *Server) handlePageview(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("DNT") == "1" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if !s.pvLimit.Allow(s.clientIP(r)) {
		writeErr(w, http.StatusTooManyRequests, "too many requests")
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
	if err := s.store.RecordPageview(req.Path, req.Referrer, s.ipHash(r), r.UserAgent()); err != nil {
		s.log.Error("record pageview", "err", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
