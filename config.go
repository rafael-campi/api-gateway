package main

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// Config comes from environment variables, with defaults for local development.
type Config struct {
	Port             string
	EcommerceURL     *url.URL
	PSPURL           *url.URL
	WebURL           *url.URL // the store's front-end; nil: "/" answers 404
	SimulatorEnabled bool     // mounts /simulator/*; never in production (ADR 0001 of the PSP)
	RateLimitRPS     float64  // per API key on /v1
	RateLimitBurst   int
	CORSOrigins      []string // empty: no CORS headers
	MaxBodyBytes     int64
}

func LoadConfig() (Config, error) {
	ecommerce, err := parseURL("ECOMMERCE_URL", "http://localhost:3000")
	if err != nil {
		return Config{}, err
	}
	psp, err := parseURL("PSP_URL", "http://localhost:3001")
	if err != nil {
		return Config{}, err
	}
	var web *url.URL
	if os.Getenv("WEB_URL") != "" {
		if web, err = parseURL("WEB_URL", ""); err != nil {
			return Config{}, err
		}
	}
	rps, err := strconv.ParseFloat(env("RATE_LIMIT_RPS", "10"), 64)
	if err != nil || rps <= 0 {
		return Config{}, fmt.Errorf("RATE_LIMIT_RPS must be a positive number")
	}
	burst, err := strconv.Atoi(env("RATE_LIMIT_BURST", "20"))
	if err != nil || burst <= 0 {
		return Config{}, fmt.Errorf("RATE_LIMIT_BURST must be a positive integer")
	}

	var origins []string
	for _, o := range strings.Split(os.Getenv("CORS_ALLOWED_ORIGINS"), ",") {
		if o = strings.TrimSpace(o); o != "" {
			origins = append(origins, o)
		}
	}

	return Config{
		Port:             env("PORT", "8080"),
		EcommerceURL:     ecommerce,
		PSPURL:           psp,
		WebURL:           web,
		SimulatorEnabled: os.Getenv("SIMULATORS_ENABLED") == "true",
		RateLimitRPS:     rps,
		RateLimitBurst:   burst,
		CORSOrigins:      origins,
		MaxBodyBytes:     1 << 20,
	}, nil
}

func env(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

func parseURL(name, fallback string) (*url.URL, error) {
	u, err := url.Parse(env(name, fallback))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("%s must be an absolute URL", name)
	}
	return u, nil
}
