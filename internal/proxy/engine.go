package proxy

import (
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/krishjj8/go-reverse-proxy/internal/config"
)

type CircuitState int

const (
	StateClosed CircuitState = iota
	StateOpen
	StateHalfOpen
)

type CircuitBreaker struct {
	state           CircuitState
	failureCount    int
	threshold       int
	cooldownWindow  time.Duration
	lastStateChange time.Time
	mu              sync.Mutex
}

func NewCircuitBreaker(threshold int, cooldown time.Duration) *CircuitBreaker {
	return &CircuitBreaker{
		state:           StateClosed,
		threshold:       threshold,
		cooldownWindow:  cooldown,
		lastStateChange: time.Now(),
	}
}

func (cb *CircuitBreaker) Allow() bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	if cb.state == StateOpen {
		if time.Since(cb.lastStateChange) >= cb.cooldownWindow {
			slog.Info("Circuit breaker shifting to HALF-OPEN canary state. Testing upstream wire.", "cooldown_expired_seconds", cb.cooldownWindow.Seconds())
			cb.state = StateHalfOpen
			cb.lastStateChange = time.Now()
			return true
		}
		return false
	}

	return true
}

func (cb *CircuitBreaker) RecordSuccess() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.failureCount = 0

	if cb.state == StateHalfOpen {
		slog.Info("Canary trial request succeeded! Shifting circuit breaker back to CLOSED.")
		cb.state = StateClosed
		cb.lastStateChange = time.Now()
	}
}

func (cb *CircuitBreaker) RecordFailure() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.failureCount++

	if cb.state == StateClosed && cb.failureCount >= cb.threshold {
		slog.Warn("Consecutive failure threshold breached! TRIPPING CIRCUIT BREAKER OPEN.", "failures", cb.failureCount, "threshold", cb.threshold)
		cb.state = StateOpen
		cb.lastStateChange = time.Now()
	} else if cb.state == StateHalfOpen {
		slog.Warn("Canary check failed while in HALF-OPEN. Resetting cooldown timer and ripping circuit OPEN.")
		cb.state = StateOpen
		cb.lastStateChange = time.Now()
	}
}

type RetryTransport struct {
	underlying http.RoundTripper
	pool       *UpstreamPool
	telemetry  *TelemetryEngine
}

func (t *RetryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	isIdempotent := req.Method == "GET" || req.Method == "HEAD"

	maxAttempts := 1
	if isIdempotent {
		maxAttempts = 3
	}

	var resp *http.Response
	var err error

	startTime := time.Now()

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if attempt > 1 {
			backoff := time.Duration(attempt-1) * 50 * time.Millisecond
			slog.Warn("Retrying failed request to alternative upstream",
				"attempt", attempt-1,
				"backoff_ms", backoff.Milliseconds(),
				"path", req.URL.Path,
			)
			time.Sleep(backoff)

			nextUpstream := t.pool.Next()
			if nextUpstream == "" {
				return nil, errors.New("all upstreams exhausted during retry execution loop")
			}

			nextURL, parseErr := url.Parse(nextUpstream)
			if parseErr == nil {
				req.URL.Scheme = nextURL.Scheme
				req.URL.Host = nextURL.Host
				req.Host = nextURL.Host
			}
		}

		resp, err = t.underlying.RoundTrip(req)
		currentUpstream := req.URL.Scheme + "://" + req.URL.Host

		if err == nil && resp.StatusCode < 500 {
			if breaker, exists := t.pool.breakers[currentUpstream]; exists {
				breaker.RecordSuccess()
			}

			t.telemetry.LogQueue <- LogEntry{
				Timestamp:  time.Now(),
				Path:       req.URL.Path,
				TargetHost: currentUpstream,
				StatusCode: resp.StatusCode,
				LatencyMs:  time.Since(startTime).Milliseconds(),
			}
			return resp, nil
		}

		if breaker, exists := t.pool.breakers[currentUpstream]; exists {
			breaker.RecordFailure()
		}

		if err == nil && resp.StatusCode >= 500 && attempt < maxAttempts {
			resp.Body.Close()
		}
	}

	finalStatus := 502
	if resp != nil {
		finalStatus = resp.StatusCode
	}

	t.telemetry.LogQueue <- LogEntry{
		Timestamp:  time.Now(),
		Path:       req.URL.Path,
		TargetHost: req.URL.Scheme + "://" + req.URL.Host,
		StatusCode: finalStatus,
		LatencyMs:  time.Since(startTime).Milliseconds(),
	}

	if err != nil {
		return nil, err
	}
	return resp, nil
}

type UpstreamPool struct {
	all      []string
	healthy  []string
	breakers map[string]*CircuitBreaker
	counter  int
	mu       sync.RWMutex
}

func (p *UpstreamPool) Next() string {
	p.mu.Lock()
	defer p.mu.Unlock()

	if len(p.healthy) == 0 {
		return ""
	}

	for i := 0; i < len(p.healthy); i++ {
		target := p.healthy[p.counter%len(p.healthy)]
		p.counter++

		if breaker, exists := p.breakers[target]; exists {
			if breaker.Allow() {
				return target
			}
		} else {
			return target
		}
	}
	return ""
}

type Engine struct {
	routingTable map[string]*UpstreamPool
	transport    http.RoundTripper
	telemetry    *TelemetryEngine
}

func NewEngine(cfg *config.Config) *Engine {
	customTransport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}

	telemetryEngine := NewTelemetryEngine(10000)

	table := make(map[string]*UpstreamPool)
	for host, route := range cfg.Routes {
		breakers := make(map[string]*CircuitBreaker)
		for _, upstream := range route.Upstreams {
			breakers[upstream] = NewCircuitBreaker(3, 10*time.Second)
		}

		pool := &UpstreamPool{
			all:      route.Upstreams,
			healthy:  route.Upstreams,
			breakers: breakers,
		}
		table[host] = pool
		go pool.startHealthCheckLoop()
	}

	return &Engine{
		routingTable: table,
		transport:    customTransport,
		telemetry:    telemetryEngine,
	}
}

func (p *UpstreamPool) startHealthCheckLoop() {
	ticker := time.NewTicker(5 * time.Second)
	client := &http.Client{
		Timeout: 2 * time.Second,
	}

	for range ticker.C {
		var liveBackends []string
		for _, upstream := range p.all {
			resp, err := client.Get(upstream + "/")
			if err == nil && resp.StatusCode == http.StatusOK {
				liveBackends = append(liveBackends, upstream)
				resp.Body.Close()
			} else {
				slog.Warn("Upstream detected as UNHEALTHY", "upstream", upstream, "reason", err)
			}
		}

		p.mu.Lock()
		p.healthy = liveBackends
		p.mu.Unlock()
	}
}

func (e *Engine) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	incomingHost := r.Host

	if strings.Contains(incomingHost, ":") {
		parts := strings.Split(incomingHost, ":")
		incomingHost = parts[0]
	}

	pool, exists := e.routingTable[incomingHost]
	if !exists || pool == nil {
		w.WriteHeader(http.StatusBadGateway)
		w.Write([]byte("502 Bad Gateway: Host routing destination unmapped.\n"))
		return
	}

	targetUpstream := pool.Next()
	if targetUpstream == "" {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte("503 Service Unavailable: All upstream targets tripped open.\n"))
		return
	}

	targetURL, err := url.Parse(targetUpstream)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	proxyHandler := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = targetURL.Scheme
			req.URL.Host = targetURL.Host
			req.URL.Path = singleJoiningSlash(targetURL.Path, req.URL.Path)
			req.Host = targetURL.Host
			removeHopByHopHeaders(req.Header)
		},
		Transport: &RetryTransport{
			underlying: e.transport,
			pool:       pool,
			telemetry:  e.telemetry,
		},
	}

	proxyHandler.ServeHTTP(w, r)
}

func removeHopByHopHeaders(h http.Header) {
	hopHeaders := []string{
		"Connection",
		"Keep-Alive",
		"Proxy-Authenticate",
		"Proxy-Authorization",
		"TE",
		"Trailers",
		"Transfer-Encoding",
		"Upgrade",
	}
	for _, header := range hopHeaders {
		h.Del(header)
	}
}

func singleJoiningSlash(a, b string) string {
	aslash := strings.HasSuffix(a, "/")
	bslash := strings.HasPrefix(b, "/")
	switch {
	case aslash && bslash:
		return a + b[1:]
	case !aslash && !bslash:
		return a + "/" + b
	}
	return a + b
}
