package turnstile

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestVerifierSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("ParseForm: %v", err)
		}
		if r.Form.Get("secret") != "secret-1" {
			t.Errorf("secret = %q", r.Form.Get("secret"))
		}
		if r.Form.Get("response") != "tok-1" {
			t.Errorf("response = %q", r.Form.Get("response"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true})
	}))
	defer srv.Close()

	v := NewHTTP("secret-1", srv.URL)
	ok, err := v.Verify(context.Background(), "tok-1", "1.2.3.4")
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !ok {
		t.Error("Verify = false, want true")
	}
}

func TestVerifierFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"success": false, "error-codes": []string{"timeout-or-duplicate"}})
	}))
	defer srv.Close()

	v := NewHTTP("secret-1", srv.URL)
	ok, err := v.Verify(context.Background(), "bad", "")
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if ok {
		t.Error("Verify = true, want false")
	}
}

func TestVerifierTransportError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	v := NewHTTP("secret-1", srv.URL)
	if _, err := v.Verify(context.Background(), "tok", ""); err == nil {
		t.Fatal("non-200 siteverify must return error (fail-closed at handler)")
	}
}

func TestFakeVerifier(t *testing.T) {
	f := &Fake{Result: true}
	ok, err := f.Verify(context.Background(), "anything", "")
	if err != nil || !ok {
		t.Errorf("fake = %v, %v", ok, err)
	}
	f.Result = false
	ok, _ = f.Verify(context.Background(), "anything", "")
	if ok {
		t.Error("fake should return false after Result=false")
	}
}

func TestNoopSkips(t *testing.T) {
	n := Noop{}
	ok, err := n.Verify(context.Background(), "", "")
	if err != nil || !ok {
		t.Error("Noop must always allow (dev mode)")
	}
}
