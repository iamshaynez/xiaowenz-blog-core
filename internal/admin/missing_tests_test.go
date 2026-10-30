package admin

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Oversized uploads must be rejected. The test shrinks the handler's limit
// instead of generating a real 100MB payload — the enforcement path
// (http.MaxBytesReader) is identical.
func TestMediaUploadOversizedRejected(t *testing.T) {
	fx := newAdminFixture(t)
	h := NewHandler(Deps{Store: fx.store, Config: fx.cfg, MediaClient: fx.mediaClient, Sender: fx.sender,
		Logger: slog.New(slog.DiscardHandler)})
	h.maxUpload = 16 // bytes
	handler := h.Routes()
	session := login(t, handler)

	payload := make([]byte, 1024) // 1KB > 16B limit
	for i := range payload {
		payload[i] = 'a'
	}
	b, ct := makeMultipart(t, "file", "big.png", "image/png", payload)
	req := authed(http.MethodPost, "/admin/media", b, session)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest && rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized upload: status = %d, want 400/413", rec.Code)
	}
	// Nothing may be stored.
	if media, _ := fx.store.ListMedia(); len(media) != 0 {
		t.Errorf("oversized upload stored media: %+v", media)
	}
}

func TestSubscribersPaginationHasMore(t *testing.T) {
	fx := newAdminFixture(t)
	session := login(t, fx.handler)
	// 51 subscribers = one full page + one spillover row.
	for i := 0; i < 51; i++ {
		if _, _, _, err := fx.store.ImportSubscribers([]string{fmt.Sprintf("user%d@x.com", i)}); err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
	}
	n, _ := fx.store.CountSubscribers("", "")
	if n != 51 {
		t.Fatalf("seeded %d subscribers, want 51", n)
	}

	rec := httptest.NewRecorder()
	fx.handler.ServeHTTP(rec, authed(http.MethodGet, "/admin/subscribers", nil, session))
	if rec.Code != http.StatusOK {
		t.Fatalf("page 1: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "下一页") {
		t.Error("page 1 of 51 rows must show a next-page link (HasMore)")
	}

	rec = httptest.NewRecorder()
	fx.handler.ServeHTTP(rec, authed(http.MethodGet, "/admin/subscribers?page=2", nil, session))
	if strings.Contains(rec.Body.String(), "下一页") {
		t.Error("page 2 (1 row) must not show a next-page link")
	}
}
