package main

import (
	"net/http"
	"testing"
	"time"
)

func TestNewHTTPServerSetsFullTimeouts(t *testing.T) {
	srv := newHTTPServer(":0", http.NewServeMux())
	if srv.ReadHeaderTimeout <= 0 {
		t.Error("ReadHeaderTimeout must be set")
	}
	if srv.ReadTimeout <= 0 {
		t.Error("ReadTimeout must be set")
	}
	if srv.WriteTimeout <= 0 {
		t.Error("WriteTimeout must be set")
	}
	if srv.IdleTimeout <= 0 {
		t.Error("IdleTimeout must be set")
	}
	// ReadTimeout must comfortably cover a 100MB media upload on a slow link.
	if srv.ReadTimeout < 2*time.Minute {
		t.Errorf("ReadTimeout = %v, too tight for 100MB uploads", srv.ReadTimeout)
	}
	if srv.Addr != ":0" {
		t.Errorf("Addr = %q", srv.Addr)
	}
}
