# ./cmd/proxy/main.go

```go
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/krishjj8/go-reverse-proxy/internal/config"
	"github.com/krishjj8/go-reverse-proxy/internal/logger"
	"github.com/krishjj8/go-reverse-proxy/internal/proxy"
)

func main() {
	logger.InitLogger()

	cfg, err := config.LoadConfig("config.yaml")
	if err != nil {
		slog.Error("Configuration subsystem boot failure", "error", err)
		panic(err)
	}

	engine := proxy.NewEngine(cfg)
	limiter := proxy.NewRateLimiter(10, 20)

	slog.Info("Initializing isolated admin control plane", "port", "9090")
	proxy.StartAdminServer(":9090")

	server := &http.Server{
		Addr:         cfg.Server.ListenAddress,
		Handler:      limiter.Middleware(engine),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
	}

	go func() {
		slog.Info("Proxy engine boot cycle complete", "bind_address", server.Addr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("Network socket bind failure", "error", err)
			panic(err)
		}
	}()

	shutdownSignalChan := make(chan os.Signal, 1)
	signal.Notify(shutdownSignalChan, os.Interrupt, syscall.SIGTERM)

	caughtSignal := <-shutdownSignalChan
	slog.Warn("Termination signal intercepted by proxy gateway matrix. Initiating safety procedures.", "signal", caughtSignal.String())

	shutdownContext, cancelCloseWindow := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelCloseWindow()

	if err := server.Shutdown(shutdownContext); err != nil {
		slog.Error("Server forced shutdown executed due to timeout boundary expiration", "error", err)
	}

	slog.Info("All user connections completed cleanly. Gateway network perimeter deactivated.")
}

```

# ./config.yaml

```yaml
server:
  listen_address: ":8080"

routes:
  api.proxy:
    upstreams:
      - "http://payment-svc-stable:8001" 
      - "http://payment-svc-canary:8001" 
```

# ./Dockerfile

```dockerfile
FROM golang:1.26-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download && go mod verify

COPY . .

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -ldflags="-w -s" -o /app/proxy ./cmd/proxy/main.go



#Production

FROM gcr.io/distroless/static-debian12

WORKDIR /

COPY --from=builder /app/proxy /proxy
COPY --from=builder /app/config.yaml /config.yaml

EXPOSE 8080 9090

ENTRYPOINT ["/proxy"]
```

# ./.github/workflows/ci.yml

```yaml
name: CI/CD Pipeline

on:
  push:
    branches: [ main ]
  pull_request:
    branches: [ main ]

jobs:
  validate-and-build:
    runs-on: ubuntu-latest

    steps:
    - name: Checkout Source Code
      uses: actions/checkout@v4

    # WEEK 5 UPGRADE: Bump version from 1.24 to 1.26 to match module requirements
    - name: Initialize Go Toolchain
      uses: actions/setup-go@v5
      with:
        go-version: '1.26'
        cache: true

    - name: Verify Dependencies
      run: go mod download && go mod verify

    - name: Check Formatting
      run: |
        if [ -n "$(gofmt -l .)" ]; then
          echo "Files not formatted:"
          gofmt -l .
          exit 1
        fi

    - name: Build Linux Binary
      run: |
        GOOS=linux GOARCH=amd64 go build -o proxy-binary ./cmd/proxy/...

    - name: Upload Binary
      uses: actions/upload-artifact@v4
      with:
        name: proxy-binary
        path: proxy-binary

  deploy:
    needs: validate-and-build
    runs-on: ubuntu-latest
    if: false

    steps:
    - name: Checkout Source Code
      uses: actions/checkout@v4

    - name: Download Binary
      uses: actions/download-artifact@v4
      with:
        name: proxy-binary

    - name: Setup SSH
      run: |
        mkdir -p ~/.ssh
        echo "${{ secrets.EC2_SSH_KEY }}" > ~/.ssh/deploy_key
        chmod 600 ~/.ssh/deploy_key
        ssh-keyscan -H ${{ secrets.EC2_HOST }} >> ~/.ssh/known_hosts

    - name: Copy files to EC2
      run: |
        chmod +x proxy-binary
        scp -i ~/.ssh/deploy_key proxy-binary ${{ secrets.EC2_USER }}@${{ secrets.EC2_HOST }}:~/proxy-binary
        scp -i ~/.ssh/deploy_key config.yaml ${{ secrets.EC2_USER }}@${{ secrets.EC2_HOST }}:~/config.yaml

    - name: Start proxy service
      run: |
        ssh -i ~/.ssh/deploy_key ${{ secrets.EC2_USER }}@${{ secrets.EC2_HOST }} \
          "sudo mkdir -p /opt/proxy && \
           sudo mv ~/proxy-binary /opt/proxy/proxy && \
           sudo mv ~/config.yaml /opt/proxy/config.yaml && \
           sudo chmod +x /opt/proxy/proxy && \
           sudo systemctl restart go-proxy && \
           sudo systemctl status go-proxy"

    - name: Verify proxy responding
      run: |
        sleep 5
        curl -f -H "Host: api.proxy" http://${{ secrets.EC2_HOST }}:8080/
```

# ./infra/main.tf

```terraform
terraform {
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.0"
    }
  }
}

provider "aws" {
  region = "ap-south-1"
}





resource "aws_iam_role" "proxy" {
  name = "go-proxy-role"

  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Action    = "sts:AssumeRole"
      Effect    = "Allow"
      Principal = { Service = "ec2.amazonaws.com" }
    }]
  })
}
resource "aws_vpc" "proxy_vpc" {
  cidr_block           = "10.0.0.0/16"
  enable_dns_hostnames = true
  tags = { Name = "Proxy-VPC" }
}

resource "aws_subnet" "proxy_public" {
  vpc_id                  = aws_vpc.proxy_vpc.id
  cidr_block              = "10.0.1.0/24"
  availability_zone       = "ap-south-1a"
  map_public_ip_on_launch = true
  tags = { Name = "Proxy-Public-Subnet" }
}

resource "aws_internet_gateway" "proxy_igw" {
  vpc_id = aws_vpc.proxy_vpc.id
  tags = { Name = "Proxy-IGW" }
}

resource "aws_route_table" "proxy_public" {
  vpc_id = aws_vpc.proxy_vpc.id
  route {
    cidr_block = "0.0.0.0/0"
    gateway_id = aws_internet_gateway.proxy_igw.id
  }
  tags = { Name = "Proxy-Public-RT" }
}

resource "aws_route_table_association" "proxy_public" {
  subnet_id      = aws_subnet.proxy_public.id
  route_table_id = aws_route_table.proxy_public.id
}

resource "aws_iam_role_policy_attachment" "cloudwatch" {
  role       = aws_iam_role.proxy.name
  policy_arn = "arn:aws:iam::aws:policy/CloudWatchAgentServerPolicy"
}

resource "aws_iam_instance_profile" "proxy" {
  name = "go-proxy-profile"
  role = aws_iam_role.proxy.name
}

# Security group for the proxy instance
resource "aws_security_group" "proxy" {
  name        = "proxy-sg"
  description = "Go reverse proxy security group"
  vpc_id      = aws_vpc.proxy_vpc.id

  ingress {
    from_port   = 22
    to_port     = 22
    protocol    = "tcp"
    cidr_blocks = ["0.0.0.0/0"]
  }

  ingress {
    from_port   = 8080
    to_port     = 8080
    protocol    = "tcp"
    cidr_blocks = ["0.0.0.0/0"]
  }

  egress {
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }

  tags = { Name = "proxy-sg" }
}

# EC2 instance
resource "aws_instance" "proxy" {
  ami                         = "ami-0f5ee92e2d63afc18"
  instance_type               = "t2.micro"
  subnet_id                   = aws_subnet.proxy_public.id
  vpc_security_group_ids      = [aws_security_group.proxy.id]
  key_name                    = "demo"
  iam_instance_profile        = aws_iam_instance_profile.proxy.name
  associate_public_ip_address = true

  user_data = <<-EOF
    #!/bin/bash
    apt update -y
    apt install -y curl

    # Create directory for proxy
    mkdir -p /opt/proxy

    # Create systemd service
    cat > /etc/systemd/system/go-proxy.service <<SERVICE
    [Unit]
    Description=Go Reverse Proxy
    After=network.target

    [Service]
    Type=simple
    User=ubuntu
    WorkingDirectory=/opt/proxy
    ExecStart=/opt/proxy/proxy
    Restart=always
    RestartSec=5

    [Install]
    WantedBy=multi-user.target
    SERVICE

    systemctl daemon-reload
    systemctl enable go-proxy
  EOF

  tags = { Name = "Go-Proxy-Server" }
}

output "proxy_public_ip" {
  value       = aws_instance.proxy.public_ip
  description = "Public IP of proxy EC2 instance"
}

output "proxy_instance_id" {
  value = aws_instance.proxy.id
}
```

# ./internal/config/config.go

```go
package config

import (
	"os"

	"gopkg.in/yaml.v3"
)

type ServerConfig struct {
	ListenAddress string `yaml:"listen_address"`
}

type RouteConfig struct {
	Upstreams []string `yaml:"upstreams"`
}

type Config struct {
	Server ServerConfig           `yaml:"server"`
	Routes map[string]RouteConfig `yaml:"routes"`
}

func LoadConfig(path string) (*Config, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err

	}

	defer file.Close()

	var cfg Config

	decoder := yaml.NewDecoder(file)
	if err := decoder.Decode(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

```

# ./internal/logger/logger.go

```go
package logger

import (
	"log/slog"
	"os"
)

func InitLogger() {

	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})

	logger := slog.New(handler)
	slog.SetDefault(logger)
}

```

# ./internal/proxy/admin.go

```go
package proxy

import (
	"errors"
	"log/slog"
	"net/http"
	"net/http/pprof"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func StartAdminServer(addr string) {
	adminMux := http.NewServeMux()

	adminMux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	adminMux.Handle("/metrics", promhttp.Handler())

	adminMux.HandleFunc("/debug/pprof/", pprof.Index)
	adminMux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	adminMux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	adminMux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	adminMux.HandleFunc("/debug/pprof/trace", pprof.Trace)

	go func() {
		slog.Info("Admin control plane listening actively", "bind_address", addr)
		if err := http.ListenAndServe(addr, adminMux); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("Admin control plane socket crash", "error", err)
		}
	}()
}

```

# ./internal/proxy/engine.go

```go
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

```

# ./internal/proxy/metrics.go

```go
package proxy

import (
	"context"
	"log/slog"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
)

type CloudWatchMetrics struct {
	client    *cloudwatch.Client
	namespace string
}

func NewCloudWatchMetrics(namespace string) *CloudWatchMetrics {
	cfg, err := config.LoadDefaultConfig(context.TODO())
	if err != nil {
		slog.Warn("AWS SDK default configuration load failed. Falling back to local logging mode.", "error", err)
		return &CloudWatchMetrics{client: nil, namespace: namespace}
	}

	return &CloudWatchMetrics{
		client:    cloudwatch.NewFromConfig(cfg),
		namespace: namespace,
	}
}

func (m *CloudWatchMetrics) RecordLatency(upstream string, latencyMs float64) {
	if m.client == nil {
		return
	}

	_, err := m.client.PutMetricData(context.TODO(), &cloudwatch.PutMetricDataInput{
		Namespace: aws.String(m.namespace),
		MetricData: []types.MetricDatum{
			{
				MetricName: aws.String("ProxyLatency"),
				Value:      aws.Float64(latencyMs),
				Unit:       types.StandardUnitMilliseconds,
				Timestamp:  aws.Time(time.Now()),
				Dimensions: []types.Dimension{
					{
						Name:  aws.String("Upstream"),
						Value: aws.String(upstream),
					},
				},
			},
		},
	})
	if err != nil {
		slog.Error("Failed to stream metric point to AWS CloudWatch API", "error", err)
	}
}

```

# ./internal/proxy/ratelimiter.go

```go
package proxy

import (
	"net"
	"net/http"
	"sync"

	"golang.org/x/time/rate"
)

type RateLimiter struct {
	limiters map[string]*rate.Limiter
	mu       sync.Mutex
	rate     rate.Limit
	burst    int
}

func NewRateLimiter(r rate.Limit, burst int) *RateLimiter {
	return &RateLimiter{
		limiters: make(map[string]*rate.Limiter),
		rate:     r,
		burst:    burst,
	}
}

func (rl *RateLimiter) getLimiter(ip string) *rate.Limiter {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	limiter, exists := rl.limiters[ip]
	if !exists {
		limiter = rate.NewLimiter(rl.rate, rl.burst)
		rl.limiters[ip] = limiter
	}

	return limiter
}

func (rl *RateLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			http.Error(w, "Internal rate limiting extraction error", http.StatusInternalServerError)
			return
		}

		if !rl.getLimiter(ip).Allow() {
			http.Error(w, "Rate limit exceeded. Too many requests.", http.StatusTooManyRequests)
			return
		}

		next.ServeHTTP(w, r)
	})
}

```

# ./internal/proxy/telemetry.go

```go
package proxy

import (
	"log/slog"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

var (
	RequestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "proxy_requests_total",
			Help: "Cumulative count of all inbound HTTP requests processed by the gateway edge",
		},
		[]string{"upstream", "status_code"},
	)

	LatencyHistogram = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "proxy_request_duration_ms",
			Help:    "End-to-end request latency profile tracking in milliseconds",
			Buckets: []float64{5, 10, 25, 50, 100, 250, 500, 1000},
		},
		[]string{"upstream"},
	)
)

func init() {

	prometheus.MustRegister(RequestsTotal, LatencyHistogram)
}

type LogEntry struct {
	Timestamp  time.Time `json:"timestamp"`
	Path       string    `json:"path"`
	TargetHost string    `json:"target_host"`
	StatusCode int       `json:"status_code"`
	LatencyMs  int64     `json:"latency_ms"`
}

type TelemetryEngine struct {
	LogQueue chan LogEntry
	metrics  *CloudWatchMetrics
}

func NewTelemetryEngine(bufferSize int) *TelemetryEngine {
	engine := &TelemetryEngine{
		LogQueue: make(chan LogEntry, bufferSize),
		metrics:  NewCloudWatchMetrics("ReverseProxyGateway"),
	}

	go engine.startLogConsumer()

	return engine
}

func (te *TelemetryEngine) startLogConsumer() {
	slog.Info("Asynchronous cloud telemetry background worker initialized cleanly.")

	for entry := range te.LogQueue {
		slog.Info("Telemetry event dispatched asynchronously",
			"path", entry.Path,
			"target", entry.TargetHost,
			"status", entry.StatusCode,
			"latency_ms", entry.LatencyMs,
		)

		te.metrics.RecordLatency(entry.TargetHost, float64(entry.LatencyMs))

		statusCodeStr := strconv.Itoa(entry.StatusCode)

		RequestsTotal.WithLabelValues(entry.TargetHost, statusCodeStr).Inc()

		LatencyHistogram.WithLabelValues(entry.TargetHost).Observe(float64(entry.LatencyMs))
	}
}

```

# ./k8s/backend-security-policy.yaml

```yaml
apiVersion: "cilium.io/v2"
kind: CiliumNetworkPolicy
metadata:
  name: secure-proxy-perimeter
  namespace: default
spec:
  endpointSelector:
    matchLabels:
      app: go-reverse-proxy
  ingress:
  - toPorts:
    - ports:
      - port: "8080"
        protocol: TCP

  - fromEntities:
    - host          
    fromEndpoints:
    - matchLabels:
        app: prometheus
    toPorts:
    - ports:
      - port: "9090"
        protocol: TCP
```

# ./k8s/configmap.yaml

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: proxy-config
  namespace: default
data:
  config.yaml: |
    server:
      listen_address: ":8080"
    routes:
      api.proxy:
        upstreams:
          - "http://payment-svc-stable:8001"
          - "http://payment-svc-canary:8001"
```

# ./k8s/deployment.yaml

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: go-reverse-proxy
  namespace: default
  labels:
    app: go-reverse-proxy
spec:
  replicas: 2 
  selector:
    matchLabels:
      app: go-reverse-proxy
  template:
    metadata:
      labels:
        app: go-reverse-proxy
    spec:
      containers:
      - name: proxy-gateway
        image: go-reverse-proxy:latest
        imagePullPolicy: Never 
        ports:
        - containerPort: 8080
          name: public-traffic
        - containerPort: 9090
          name: admin-control
        
        
        resources:
          limits:
            cpu: "200m"      
            memory: "128Mi"  
          requests:
            cpu: "50m"       
            memory: "32Mi"   

        
        startupProbe:
          httpGet:
            path: /healthz
            port: 9090
          initialDelaySeconds: 2
          periodSeconds: 5
          failureThreshold: 3

        readinessProbe:
          httpGet:
            path: /healthz
            port: 9090
          periodSeconds: 10
          timeoutSeconds: 2
          successThreshold: 1
          failureThreshold: 3
        
        livenessProbe:
          httpGet:
            path: /healthz
            port: 9090
          initialDelaySeconds: 5
          periodSeconds: 10
          timeoutSeconds: 2
          failureThreshold: 3
        
        volumeMounts:
        - name: config-volume
          mountPath: /config.yaml
          subPath: config.yaml 
      
      volumes:
      - name: config-volume
        configMap:
          name: proxy-config
---
apiVersion: v1
kind: Service
metadata:
  name: go-reverse-proxy-service
  namespace: default
spec:
  type: ClusterIP 
  ports:
    - port: 8080
      targetPort: 8080
      protocol: TCP
      name: web-ingress
    - port: 9090
      targetPort: 9090
      protocol: TCP
      name: metrics-scrape
  selector:
    app: go-reverse-proxy
---
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: go-reverse-proxy-ingress
  namespace: default
  annotations:
    nginx.ingress.kubernetes.io/rewrite-target: /
spec:
  rules:
  - host: api.proxy
    http:
      paths:
      - path: /
        pathType: Prefix
        backend:
          service:
            name: go-reverse-proxy-service
            port:
              number: 8080
```

# ./k8s/proxy-securiy-policy.yaml

```yaml
apiVersion: "cilium.io/v2"
kind: CiliumNetworkPolicy
metadata:
  name: secure-proxy-perimeter
  namespace: default
spec:
  endpointSelector:
    matchLabels:
      app: go-reverse-proxy
  ingress:
  - toPorts:
    - ports:
      - port: "8080"
        protocol: TCP

  - fromEntities:
    - host         
    fromEndpoints:
    - matchLabels:
        app: prometheus 
    toPorts:
    - ports:
      - port: "9090"
        protocol: TCP
```

# ./kind-config.yaml

```yaml
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
networking:
  disableDefaultCNI: true
  kubeProxyMode: "none"
nodes:
-  role: control-plane
-  role: worker
```

