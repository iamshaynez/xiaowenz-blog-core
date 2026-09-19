package httpapi

import (
	"net/http"
	"strings"
)

// handleDownload serves SPEC §3.3: media lookup → dedup record → 302 to R2.
// The mux wildcard cannot express "/a/{slug}.mp3" (wildcards span whole
// segments), so the slug is parsed from the path here.
func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/a/")
	slug, ok := strings.CutSuffix(name, ".mp3")
	if !ok || slug == "" || strings.Contains(slug, "/") {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	m, err := s.store.GetMediaByKey("audio/" + slug + ".mp3")
	if err != nil {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	counted, err := s.store.RecordDownloadDedup(slug, s.ipHash(r), r.UserAgent(), r.Referer())
	if err != nil {
		s.log.Error("record download", "err", err, "slug", slug)
	} else if counted {
		s.log.Info("download counted", "slug", slug)
	}
	http.Redirect(w, r, m.URL, http.StatusFound)
}
