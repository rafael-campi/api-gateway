package main

import (
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"
)

// NewGateway routes the public entry point to the services behind it
// (docs/adr/0001). It holds no business rules and does not authenticate:
// each service checks its own credentials.
func NewGateway(cfg Config, logger *slog.Logger) http.Handler {
	ecommerce := newProxy(cfg.EcommerceURL, "/api", logger)
	psp := newProxy(cfg.PSPURL, "", logger)
	// what the shopper's browser may reach on the PSP, under /psp (as the
	// store's dev server proxies it): card tokenization, and the simulator
	pspForBrowser := newProxy(cfg.PSPURL, "/psp", logger)
	limiter := NewRateLimiter(cfg.RateLimitRPS, cfg.RateLimitBurst, time.Now)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	mux.Handle("/api/", ecommerce)
	// the PSP's merchant API: rate limited per API key
	mux.Handle("/v1/", limiter.Middleware(apiKeyOrClientIP, psp))
	mux.Handle("/rails/", psp)
	mux.Handle("POST /psp/v1/tokens", limiter.Middleware(apiKeyOrClientIP, pspForBrowser))
	if cfg.SimulatorEnabled {
		mux.Handle("/simulator/", psp)
		mux.Handle("/psp/simulator/", pspForBrowser)
	}
	// /psp/* other than the above stays closed: the PSP's operator routes
	// (/platform, /ledger) must never be reachable from outside
	mux.HandleFunc("/psp/", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONError(w, http.StatusNotFound, "not_found", "No route for this path.")
	})
	if cfg.WebURL != nil {
		mux.Handle("/", newProxy(cfg.WebURL, "", logger))
	} else {
		mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
			writeJSONError(w, http.StatusNotFound, "not_found", "No route for this path.")
		})
	}

	var handler http.Handler = mux
	handler = limitBody(cfg.MaxBodyBytes, handler)
	handler = cors(cfg.CORSOrigins, handler)
	handler = accessLog(logger, handler)
	handler = requestID(handler)
	return handler
}

// newProxy forwards to target, dropping stripPrefix from the path
// ("/api/products" reaches the e-commerce as "/products").
func newProxy(target *url.URL, stripPrefix string, logger *slog.Logger) http.Handler {
	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			if stripPrefix != "" {
				pr.Out.URL.Path = strings.TrimPrefix(pr.Out.URL.Path, stripPrefix)
				pr.Out.URL.RawPath = ""
			}
			pr.SetURL(target)
			pr.SetXForwarded()
			pr.Out.Header.Set(requestIDHeader, pr.In.Header.Get(requestIDHeader))
		},
		Transport: &http.Transport{
			Proxy:                 nil, // never route service traffic through an env proxy
			MaxIdleConnsPerHost:   32,
			IdleConnTimeout:       90 * time.Second,
			ResponseHeaderTimeout: 25 * time.Second,
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			logger.Error("upstream unavailable",
				"upstream", target.Host,
				"request_id", r.Header.Get(requestIDHeader),
				"error", err.Error(),
			)
			writeJSONError(w, http.StatusBadGateway, "upstream_unavailable",
				"The service behind this route did not answer.")
		},
	}
}
