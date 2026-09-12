// Package httpclient builds shared, tuned HTTP clients.
//
// One process → one *http.Client per provider. Never create a client per
// run: connection reuse (KeepAlive, idle pool, TLS reuse) is where the
// latency and socket-count wins come from.
package httpclient

import (
	"net"
	"net/http"
	"time"
)

// Config carries the transport tuning knobs.
type Config struct {
	MaxIdleConns          int
	MaxIdleConnsPerHost   int
	MaxConnsPerHost       int
	IdleConnTimeout       time.Duration
	TLSHandshakeTimeout   time.Duration
	ResponseHeaderTimeout time.Duration
	ExpectContinueTimeout time.Duration
	DialTimeout           time.Duration
	KeepAlive             time.Duration
}

// Default returns the tuning used for provider calls (Aily etc.).
func Default() Config {
	return Config{
		MaxIdleConns:          200,
		MaxIdleConnsPerHost:   64,
		MaxConnsPerHost:       0, // unlimited; bounded by rate limiter + inflight
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		DialTimeout:           10 * time.Second,
		KeepAlive:             30 * time.Second,
	}
}

// New builds an *http.Client with the configured transport. The client is
// meant to be shared process-wide and never closed.
func New(cfg Config, overallTimeout time.Duration) *http.Client {
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   cfg.DialTimeout,
			KeepAlive: cfg.KeepAlive,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          cfg.MaxIdleConns,
		MaxIdleConnsPerHost:   cfg.MaxIdleConnsPerHost,
		MaxConnsPerHost:       cfg.MaxConnsPerHost,
		IdleConnTimeout:       cfg.IdleConnTimeout,
		TLSHandshakeTimeout:   cfg.TLSHandshakeTimeout,
		ResponseHeaderTimeout: cfg.ResponseHeaderTimeout,
		ExpectContinueTimeout: cfg.ExpectContinueTimeout,
	}
	return &http.Client{
		Transport: transport,
		Timeout:   overallTimeout,
	}
}
