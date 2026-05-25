package proxy

import (
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"github.com/krishjj8/go-reverse-proxy/internal/config"
)

type Engine struct {
	routingTable map[string][]string
}

func NewEngine(cfg *config.Config) *Engine {
	table := make(map[string][]string)
	for host, route := range cfg.Routes {
		table[host] = route.Upstreams
	}
	return &Engine{routingTable: table}
}

func (e *Engine) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	incomingHost := r.Host

	if strings.Contains(incomingHost, ":") {
		parts := strings.Split(incomingHost, ":")
		incomingHost = parts[0]
	}

	upstreams, exists := e.routingTable[incomingHost]
	if !exists || len(upstreams) == 0 {
		w.WriteHeader(http.StatusBadGateway)
		w.Write([]byte("502 Bad Gateway: Host routing destination unmapped.\n"))
		return
	}

	targetUpstream := upstreams[0]

	// 1. Parse the target upstream string into a concrete *url.URL structure
	targetURL, err := url.Parse(targetUpstream)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("500 Internal Server Error: Malformed upstream target configuration.\n"))
		return
	}

	// 2. Initialize the production-grade Reverse Proxy configuration engine
	proxyHandler := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			// Mutate the outbound packet destination fields
			req.URL.Scheme = targetURL.Scheme
			req.URL.Host = targetURL.Host
			req.URL.Path = singleJoiningSlash(targetURL.Path, req.URL.Path)

			// Crucial step: Rewrite the top-level Host header string for the backend server
			req.Host = targetURL.Host

			// 3. Security Sanity Check: Scrub connection-breaking hop-by-hop headers
			removeHopByHopHeaders(req.Header)
		},
	}

	// 4. Trigger the standard library to execute the actual TCP forwarding loop!
	proxyHandler.ServeHTTP(w, r)
}

// removeHopByHopHeaders wipes out transport-layer headers that shouldn't pass to backends
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

// singleJoiningSlash safely cleans up path concatenations (preventing "//some/path" bugs)
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
