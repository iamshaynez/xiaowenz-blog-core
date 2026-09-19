// Package turnstile verifies Cloudflare Turnstile tokens.
package turnstile

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Verifier is the captcha seam; tests use Fake, dev uses Noop.
type Verifier interface {
	Verify(ctx context.Context, token, remoteIP string) (bool, error)
}

// Fake returns a fixed result for tests.
type Fake struct {
	Result bool
	Err    error
}

// Verify implements Verifier.
func (f *Fake) Verify(_ context.Context, _, _ string) (bool, error) { return f.Result, f.Err }

// Noop always allows — dev mode when CORE_TURNSTILE_SECRET is empty.
type Noop struct{}

// Verify implements Verifier.
func (Noop) Verify(_ context.Context, _, _ string) (bool, error) { return true, nil }

const defaultSiteverifyURL = "https://challenges.cloudflare.com/turnstile/v0/siteverify"

// HTTPVerifier calls the Cloudflare siteverify endpoint.
type HTTPVerifier struct {
	secret string
	url    string
	client *http.Client
}

// NewHTTP builds a verifier; url is injectable for loopback tests.
func NewHTTP(secret, siteverifyURL string) *HTTPVerifier {
	if siteverifyURL == "" {
		siteverifyURL = defaultSiteverifyURL
	}
	return &HTTPVerifier{
		secret: secret,
		url:    siteverifyURL,
		client: &http.Client{Timeout: 5 * time.Second},
	}
}

// Verify posts the token to siteverify. Transport errors are returned so the
// handler can fail closed.
func (v *HTTPVerifier) Verify(ctx context.Context, token, remoteIP string) (bool, error) {
	form := url.Values{
		"secret":   {v.secret},
		"response": {token},
	}
	if remoteIP != "" {
		form.Set("remoteip", remoteIP)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, v.url,
		strings.NewReader(form.Encode()))
	if err != nil {
		return false, fmt.Errorf("siteverify request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := v.client.Do(req)
	if err != nil {
		return false, fmt.Errorf("siteverify call: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("siteverify status %d", resp.StatusCode)
	}
	var out struct {
		Success bool `json:"success"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return false, fmt.Errorf("siteverify decode: %w", err)
	}
	return out.Success, nil
}
