package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// upstream records what reached a service behind the gateway.
type upstream struct {
	*httptest.Server
	got []*http.Request
}

func newUpstream(t *testing.T, name string) *upstream {
	u := &upstream{}
	u.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.got = append(u.got, r.Clone(r.Context()))
		w.Header().Set("X-Served-By", name)
		_, _ = io.WriteString(w, name+" "+r.URL.RequestURI())
	}))
	t.Cleanup(u.Close)
	return u
}

func mustURL(t *testing.T, raw string) *url.URL {
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

type setup struct {
	gateway   http.Handler
	ecommerce *upstream
	psp       *upstream
}

func newSetup(t *testing.T, change func(*Config)) setup {
	ecommerce := newUpstream(t, "ecommerce")
	psp := newUpstream(t, "psp")
	cfg := Config{
		EcommerceURL:   mustURL(t, ecommerce.URL),
		PSPURL:         mustURL(t, psp.URL),
		RateLimitRPS:   1000,
		RateLimitBurst: 1000,
		MaxBodyBytes:   1 << 20,
	}
	if change != nil {
		change(&cfg)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return setup{NewGateway(cfg, logger), ecommerce, psp}
}

func (s setup) do(req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	s.gateway.ServeHTTP(rec, req)
	return rec
}

func TestRoutesToTheRightService(t *testing.T) {
	s := newSetup(t, func(c *Config) { c.SimulatorEnabled = true })

	cases := []struct{ method, path, want string }{
		{"GET", "/api/products?limit=5", "ecommerce /products?limit=5"},
		{"POST", "/api/webhooks/payments", "ecommerce /webhooks/payments"},
		{"GET", "/v1/payments/pay_1", "psp /v1/payments/pay_1"},
		{"POST", "/rails/pix/webhook", "psp /rails/pix/webhook"},
		{"POST", "/simulator/pix/abc/pay", "psp /simulator/pix/abc/pay"},
		{"POST", "/psp/v1/tokens", "psp /v1/tokens"},
		{"POST", "/psp/simulator/pix/abc/pay", "psp /simulator/pix/abc/pay"},
	}
	for _, c := range cases {
		rec := s.do(httptest.NewRequest(c.method, c.path, nil))
		if rec.Code != http.StatusOK || rec.Body.String() != c.want {
			t.Errorf("%s: got %d %q, want 200 %q", c.path, rec.Code, rec.Body.String(), c.want)
		}
	}
}

func TestUnknownPathsAndDisabledSimulatorAre404(t *testing.T) {
	s := newSetup(t, nil)

	for _, path := range []string{
		"/", "/products", "/platform/merchants", "/ledger/transactions",
		"/simulator/pix/abc/pay", "/psp/simulator/pix/abc/pay",
		// the browser prefix must not open the PSP's other routes
		"/psp/platform/merchants", "/psp/ledger/transactions", "/psp/v1/payments",
	} {
		rec := s.do(httptest.NewRequest(http.MethodPost, path, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: got %d, want 404", path, rec.Code)
		}
	}
	if len(s.psp.got)+len(s.ecommerce.got) != 0 {
		t.Error("a 404 must not reach any service")
	}
}

func TestForwardsBodyAndHeaders(t *testing.T) {
	s := newSetup(t, nil)
	req := httptest.NewRequest(http.MethodPost, "/v1/payments", strings.NewReader(`{"amount":100}`))
	req.Header.Set("Authorization", "Bearer sk_test_x")
	req.Header.Set("Idempotency-Key", "cart-1-attempt-1")

	s.do(req)

	got := s.psp.got[0]
	if got.Header.Get("Authorization") != "Bearer sk_test_x" ||
		got.Header.Get("Idempotency-Key") != "cart-1-attempt-1" {
		t.Errorf("headers not forwarded: %v", got.Header)
	}
	if got.Header.Get("X-Forwarded-For") == "" {
		t.Error("X-Forwarded-For not set")
	}
}

func TestRequestID(t *testing.T) {
	s := newSetup(t, nil)

	t.Run("generated and propagated", func(t *testing.T) {
		rec := s.do(httptest.NewRequest(http.MethodGet, "/api/products", nil))
		id := rec.Header().Get("X-Request-Id")
		if len(id) != 32 {
			t.Fatalf("want a 32-char id, got %q", id)
		}
		if got := s.ecommerce.got[len(s.ecommerce.got)-1].Header.Get("X-Request-Id"); got != id {
			t.Errorf("service saw %q, client saw %q", got, id)
		}
	})

	t.Run("kept when valid", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/products", nil)
		req.Header.Set("X-Request-Id", "abc-123")
		if got := s.do(req).Header().Get("X-Request-Id"); got != "abc-123" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("replaced when unsafe to log", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/products", nil)
		req.Header.Set("X-Request-Id", "bad id\nwith newline")
		if got := s.do(req).Header().Get("X-Request-Id"); len(got) != 32 {
			t.Errorf("got %q", got)
		}
	})
}

func TestRateLimitsPerAPIKeyOnV1(t *testing.T) {
	s := newSetup(t, func(c *Config) { c.RateLimitRPS = 1; c.RateLimitBurst = 2 })
	call := func(path, key string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer "+key)
		return s.do(req)
	}

	call("/v1/balance", "sk_alice")
	call("/v1/balance", "sk_alice")
	limited := call("/v1/balance", "sk_alice")

	if limited.Code != http.StatusTooManyRequests || limited.Header().Get("Retry-After") != "1" {
		t.Fatalf("got %d Retry-After=%q", limited.Code, limited.Header().Get("Retry-After"))
	}
	var body map[string]any
	_ = json.Unmarshal(limited.Body.Bytes(), &body)
	if body["error"] != "rate_limited" {
		t.Errorf("body %v", body)
	}
	if call("/v1/balance", "sk_bob").Code != http.StatusOK {
		t.Error("another key has its own bucket")
	}
	if call("/api/products", "sk_alice").Code != http.StatusOK {
		t.Error("/api is not limited by the /v1 bucket")
	}
}

func TestRateLimiterRefills(t *testing.T) {
	now := time.Unix(0, 0)
	l := NewRateLimiter(2, 1, func() time.Time { return now })

	if ok, _ := l.Allow("k"); !ok {
		t.Fatal("first call allowed")
	}
	ok, wait := l.Allow("k")
	if ok || wait != 500*time.Millisecond {
		t.Fatalf("want blocked for 500ms, got %v %v", ok, wait)
	}
	now = now.Add(500 * time.Millisecond)
	if ok, _ := l.Allow("k"); !ok {
		t.Error("refilled after 1/rate seconds")
	}
}

func TestUpstreamDownIs502(t *testing.T) {
	s := newSetup(t, nil)
	s.psp.Close()

	rec := s.do(httptest.NewRequest(http.MethodGet, "/v1/payments", nil))

	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "upstream_unavailable") {
		t.Errorf("got %d %s", rec.Code, rec.Body.String())
	}
}

func TestRejectsOversizedBodies(t *testing.T) {
	s := newSetup(t, func(c *Config) { c.MaxBodyBytes = 10 })

	rec := s.do(httptest.NewRequest(http.MethodPost, "/v1/payments", strings.NewReader(strings.Repeat("x", 11))))

	if rec.Code != http.StatusRequestEntityTooLarge || len(s.psp.got) != 0 {
		t.Errorf("got %d, psp saw %d requests", rec.Code, len(s.psp.got))
	}
}

func TestCORS(t *testing.T) {
	s := newSetup(t, func(c *Config) { c.CORSOrigins = []string{"https://loja.example"} })

	preflight := httptest.NewRequest(http.MethodOptions, "/api/products", nil)
	preflight.Header.Set("Origin", "https://loja.example")
	preflight.Header.Set("Access-Control-Request-Method", "POST")
	rec := s.do(preflight)
	if rec.Code != http.StatusNoContent || rec.Header().Get("Access-Control-Allow-Origin") != "https://loja.example" {
		t.Errorf("preflight: %d %v", rec.Code, rec.Header())
	}

	other := httptest.NewRequest(http.MethodGet, "/api/products", nil)
	other.Header.Set("Origin", "https://evil.example")
	if got := s.do(other).Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("unknown origin allowed: %q", got)
	}
}

func TestHealthz(t *testing.T) {
	s := newSetup(t, nil)
	rec := s.do(httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != `{"status":"ok"}` {
		t.Errorf("got %d %s", rec.Code, rec.Body.String())
	}
}

func TestServesTheFrontEndForEverythingElse(t *testing.T) {
	web := newUpstream(t, "web")
	s := newSetup(t, func(c *Config) { c.WebURL = mustURL(t, web.URL) })

	for _, path := range []string{"/", "/produtos/3", "/admin/financeiro"} {
		rec := s.do(httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Body.String() != "web "+path {
			t.Errorf("%s: got %q", path, rec.Body.String())
		}
	}
	// the PSP stays closed even with a front-end behind "/"
	if rec := s.do(httptest.NewRequest(http.MethodGet, "/psp/platform/merchants", nil)); rec.Code != http.StatusNotFound {
		t.Errorf("/psp/platform: got %d", rec.Code)
	}
}

func TestTokenizationIsOnlyPOST(t *testing.T) {
	s := newSetup(t, nil)

	rec := s.do(httptest.NewRequest(http.MethodGet, "/psp/v1/tokens", nil))

	if rec.Code != http.StatusNotFound && rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("got %d", rec.Code)
	}
	if len(s.psp.got) != 0 {
		t.Error("GET must not reach the PSP")
	}
}
