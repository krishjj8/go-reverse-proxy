package proxy

import (
	"errors"
	"log/slog"
	"net" // 1. Imported for custom network dialing tuning
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

type RetryTransport struct {
	underlying http.RoundTripper
	pool       *UpstreamPool
}

func (t *RetryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	isIdempotent := req.Method == "GET" || req.Method == "HEAD"

	maxAttempts := 1
	if isIdempotent {
		maxAttempts = 3
	}

	var resp *http.Response
	var err error

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

		if err == nil && resp.StatusCode < 500 {
			return resp, nil
		}

		if err == nil && resp.StatusCode >= 500 && attempt < maxAttempts {
			resp.Body.Close()
		}
	}

	if err != nil {
		return nil, err
	}
	return resp, nil
}

type UpstreamPool struct {
	all     []string
	healthy []string
	counter int
	mu      sync.RWMutex
}

func (p *UpstreamPool) Next() string {
	p.mu.Lock()
	defer p.mu.Unlock()

	if len(p.healthy) == 0 {
		return ""
	}
	target := p.healthy[p.counter%len(p.healthy)]
	p.counter++
	return target
}

type Engine struct {
	routingTable map[string]*UpstreamPool
	transport    http.RoundTripper
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

	table := make(map[string]*UpstreamPool)
	for host, route := range cfg.Routes {
		pool := &UpstreamPool{
			all:     route.Upstreams,
			healthy: route.Upstreams,
		}
		table[host] = pool
		go pool.startHealthCheckLoop()
	}

	return &Engine{
		routingTable: table,
		transport:    customTransport,
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
		return a + b
	}
	return a + "/" + b
}
