# Go Reverse Proxy Gateway

![Go](https://img.shields.io/badge/Go-1.26+-00ADD8?style=flat&logo=go&logoColor=white)
![CI](https://github.com/krishjj8/go-reverse-proxy/actions/workflows/ci.yml/badge.svg)
![Platform](https://img.shields.io/badge/platform-Minikube%20%7C%20AWS-orange?style=flat&logo=amazon-aws)
![License](https://img.shields.io/badge/license-MIT-green?style=flat)

A Layer-7 reverse proxy and API gateway written in Go. It routes traffic across multiple upstreams with round-robin load balancing, active health checks, a per-upstream circuit breaker, per-IP rate limiting, and asynchronous metrics (Prometheus pull + AWS CloudWatch push). It ships as a multi-stage distroless container with Kubernetes manifests and a Terraform-provisioned AWS deployment.

---

## Quick start

### Local (no container)

```bash
git clone https://github.com/krishjj8/go-reverse-proxy
cd go-reverse-proxy
go build -o proxy ./cmd/proxy/...

# Start two test upstreams in separate terminals
python3 -m http.server 8001
python3 -m http.server 8002

# Run the proxy
./proxy --config config.yaml

# Send a request
curl -H "Host: api.proxy" http://localhost:8080/
```

### Local Kubernetes (Minikube)

```bash
docker build -t go-reverse-proxy:latest .
minikube start --driver=docker
minikube image load go-reverse-proxy:latest

kubectl apply -f k8s/configmap.yaml
kubectl apply -f k8s/deployment.yaml
kubectl port-forward svc/go-reverse-proxy-service 8080:8080 9090:9090

# Health check (admin port)
curl -i http://localhost:9090/healthz

# Exercise the rate limiter — expect some 429s
for i in {1..30}; do
  curl -s -o /dev/null -w "%{http_code}\n" -H "Host: api.proxy" http://localhost:8080/
done
```

---

## Architecture

```
                    Client request
                          │
                          ▼
              ┌───────────────────────┐
              │   Data plane  (:8080) │
              └───────────┬───────────┘
                          ▼
              ┌───────────────────────┐
              │  Rate limiter         │
              │  (token bucket / IP)  │
              └───────────┬───────────┘
                          ▼
              ┌───────────────────────┐
              │  Circuit breaker      │
              │  closed ⇆ open ⇆ half │
              └───────────┬───────────┘
                          ▼
              ┌───────────────────────┐
              │  Round-robin balancer │
              │  [target-1][target-2] │
              └───────────┬───────────┘
                          ▼
              ┌───────────────────────┐
              │  HTTP transport pool  │
              │  (keep-alive reuse)   │
              └───────────┬───────────┘
                ┌─────────┴─────────┐
                ▼                   ▼
            Upstream A          Upstream B
                └─────────┬─────────┘
                          │  enqueue (buffered channel)
                          ▼
              ┌───────────────────────┐
              │  Telemetry worker     │
              └───────────┬───────────┘
                ┌─────────┴─────────┐
                ▼                   ▼
   Control plane (:9090)      AWS CloudWatch (push)
   ├── /healthz
   └── /metrics (Prometheus)
```

---

## Features

### Round-robin load balancing

Requests are distributed across upstreams from a pool guarded by `sync.RWMutex`, so many requests can read the upstream list concurrently while the background health check updates it.

### Circuit breaker

A three-state breaker per upstream:

- **Closed** — normal; requests are forwarded.
- **Open** — after 3 consecutive failures, requests to that upstream are short-circuited.
- **Half-open** — after a 10s cooldown, one request is allowed through to test recovery. Success closes the breaker; failure reopens it.

### Per-IP rate limiting

In-memory token bucket via `golang.org/x/time/rate`. Defaults: 10 req/s refill, burst 20. Over the limit returns `429 Too Many Requests`. (Currently hardcoded — see Known limitations.)

### Connection pooling

The HTTP transport reuses keep-alive connections (`MaxIdleConnsPerHost = 100`) to avoid a TCP/TLS handshake on every request.

### Asynchronous metrics

The request path makes no metrics or logging network calls inline. Each handled request enqueues a small record on a buffered channel, and a background worker drains it and updates two sinks:

1. **Prometheus** — in-memory counters and histograms, scraped from `/metrics`.
2. **AWS CloudWatch** — sent via `PutMetricData`.

This keeps the request path off slow external calls. (See Known limitations for the backpressure case this design doesn't yet handle.)

---

## What I learned

- **`RWMutex` vs `Mutex`** — the upstream list is read on every request and written only by the health check. A plain `Mutex` serializes everything; `RWMutex` lets reads run concurrently and only blocks during the infrequent write. The difference only shows up under load.
- **The half-open state matters** — it's easy to skip, but without it a recovered backend either never gets traffic back (stuck open) or gets flooded immediately (no protection). The single canary request is what makes recovery safe.
- **Git's object model** — I committed Terraform provider binaries that exceeded GitHub's 100MB limit, and they stayed in history even after I updated `.gitignore`. Fixing it with `git filter-branch` forced me to actually understand commits, trees, and blobs instead of just the surface commands.
- **CI on real infrastructure** — debugging SCP permission failures in GitHub Actions came down to how non-root users, sudo, and file ownership interact — things local testing never surfaced. The answer was in the systemd logs, not the Actions output.
- **Async observability as a design choice** — decoupling metrics from the request path with a producer/consumer queue keeps the hot path predictable even when CloudWatch is slow. (The remaining edge case is in Known limitations.)
- **Prometheus label series are lazy** — a metric with labels doesn't appear in `/metrics` until the first time that label combination is recorded with `WithLabelValues(...)`. So immediately after startup, before any traffic, the per-upstream series simply don't exist yet.

---

## Design decisions

### Token bucket over fixed window

Fixed-window limiters can let a burst through at a window boundary (effectively two windows' worth back to back). A token bucket smooths traffic while still allowing a controlled burst.

### Buffered channel for telemetry

Publishing metrics inline would tie request latency to CloudWatch's availability. A producer/consumer split keeps the handler lean and lets the worker handle — or drop — telemetry independently of the request path.

### Removing hop-by-hop headers

`Connection`, `Keep-Alive`, `Transfer-Encoding`, `Upgrade`, and similar headers are connection-specific per RFC 7230 and must not be forwarded by an intermediary. Forwarding them causes mismanaged keep-alive state and hard-to-debug errors at the upstream.

### Distroless runtime

`gcr.io/distroless/static` keeps the image around ~25MB and removes the shell and package manager, so there's no `/bin/sh`, `apt`, or `apk` available to an attacker if the process is ever compromised.

---

## Configuration

Edit `config.yaml` to set the listen address and upstream targets:

```yaml
server:
  listen_address: ":8080"

routes:
  api.proxy:
    upstreams:
      - "http://payment-svc:8001"
      - "http://inventory-svc:8002"
```

Routing is matched on the incoming `Host` header. Under Kubernetes, these addresses resolve to internal Service DNS names.

---

## Installation

```bash
git clone https://github.com/krishjj8/go-reverse-proxy
cd go-reverse-proxy
go mod download && go mod verify
docker build -t go-reverse-proxy:latest .
```

---

## Deployment

### Kubernetes (local, Minikube)

- Minikube using the Docker driver.
- Config is kept out of the image: the `ConfigMap` is mounted into the pod as a volume at startup, so config changes don't require a rebuild.

### AWS (single EC2)

- EC2 `t2.micro`, Ubuntu 24.04, run as a `systemd` service (`go-proxy.service`).
- Custom VPC, public subnet, internet gateway, and a security group that allows ingress on port `8080` only.
- No AWS keys in the source. CloudWatch `PutMetricData` uses temporary credentials from an IAM instance profile (IMDSv2).

---

## Observability

The proxy uses two ports: the data plane (`8080`) serves traffic; the admin plane (`9090`) serves health and metrics.

Metrics on `:9090/metrics`:

| Metric | Type | Labels | Purpose |
| --- | --- | --- | --- |
| `proxy_requests_total` | Counter | `upstream`, `status_code` | Request volume and error rates |
| `proxy_request_duration_ms` | Histogram | `upstream` | Latency distribution (p50/p90/p99) |

Metrics are updated by the background worker, so collection adds little overhead to the request path.

---

## CI

GitHub Actions runs on every push to `main`:

| Stage | Command | Purpose |
| --- | --- | --- |
| Go toolchain | `actions/setup-go@v5` (Go 1.26) | Pin the build version |
| Dependencies | `go mod verify` | Check module checksums |
| Format | `gofmt -l .` | Fail the run on unformatted code |
| Build | `go build ./cmd/proxy/...` | Compile a static Linux binary |
| Deploy | disabled (`if: false`) | SSH-to-EC2 deploy, off by default |

---

## Known limitations / next steps

- **Telemetry enqueue can block under backpressure.** The worker is a single consumer that calls CloudWatch synchronously; if CloudWatch is slow or throttles, the buffered channel fills and the enqueue on the request path blocks. Planned fix: a non-blocking send that drops (and counts) records when the buffer is full, plus a timeout on the CloudWatch call.
- **The rate-limiter map grows unbounded.** One `rate.Limiter` is kept per client IP and never evicted. Planned fix: evict idle entries (last-seen timestamp + periodic sweep, or an LRU).
- **Client IP comes from `RemoteAddr`.** Behind an ingress or load balancer, that's the proxy's address rather than the real client, so rate limiting groups everyone behind the LB. Planned fix: read `X-Forwarded-For` / `X-Real-Ip`, trusting it only from known proxies.
- **Per-request CloudWatch `PutMetricData`.** One API call per request is costly and rate-limited at volume; Prometheus is the primary metrics path. Planned: batch the calls or make CloudWatch optional.
- **Health check is a root `GET`.** It treats any non-200 as unhealthy, doesn't use a dedicated health endpoint, and doesn't close the response body on the non-200 path. Planned: a configurable health path and always closing the body.
- **Rate-limit and breaker thresholds are hardcoded.** They should move into `config.yaml`.
- **No tests yet.** The circuit breaker and rate limiter are pure logic and are the first things to unit-test, alongside adding `go vet` and `go test ./...` to CI.

---

## Debugging log

Real issues hit during development, kept for reference.

### SSH key permissions

SSH rejects keys with world-readable permissions:

```bash
mv ~/Downloads/demo.pem ~/.ssh/demo.pem
chmod 400 ~/.ssh/demo.pem
ssh -i ~/.ssh/demo.pem ubuntu@<ec2-public-ip>
```

### Removing Terraform binaries from Git history

Provider files over GitHub's 100MB limit persisted in history even after `.gitignore` was updated.

```bash
cat >> .gitignore << 'EOF'
**/.terraform/
*.tfstate
*.tfstate.backup
*.tfvars
.terraform.lock.hcl
EOF

git filter-branch --force --index-filter \
  "git rm -rf --cached --ignore-unmatch infra/.terraform/" \
  --prune-empty --tag-name-filter cat -- --all

git push origin main --force
```

### SCP deploy permissions

GitHub Actions connects as the non-root `ubuntu` user, so direct writes to `/opt` failed:

```bash
# Upload to the home directory first
scp -i key proxy-binary ubuntu@<host>:~/proxy-binary

# Then move it with sudo on the instance
sudo mkdir -p /opt/proxy
sudo mv ~/proxy-binary /opt/proxy/proxy
sudo chmod +x /opt/proxy/proxy
```

### Go version mismatch

Local builds used Go 1.26 while the Docker builder image was on 1.24, which broke `go mod download`. Fixed by pinning the builder stage to `golang:1.26-alpine`.

---

## Validation

```bash
# Admin health
curl -i http://localhost:9090/healthz

# Current metrics
curl http://localhost:9090/metrics

# Routing
curl -H "Host: api.proxy" http://localhost:8080/

# Rate limiter — expect ~20 OK responses, then 429s
hey -n 50 -c 5 -H "Host: api.proxy" http://localhost:8080/
```

---

## Project layout

```
go-reverse-proxy/
├── cmd/proxy/        # main: startup, signal handling, graceful shutdown
├── internal/proxy/   # engine: balancer, circuit breaker, rate limiter, admin server, telemetry
├── internal/config/  # YAML config loader
├── internal/logger/  # structured JSON logging (slog)
├── k8s/              # ConfigMap, Deployment, Service
├── Dockerfile        # multi-stage distroless build
├── config.yaml       # routing config
└── go.mod
```
