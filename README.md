# Go Reverse Proxy Gateway

![Go](https://img.shields.io/badge/Go-1.26+-00ADD8?style=flat&logo=go&logoColor=white)
![CI](https://github.com/krishjj8/go-reverse-proxy/actions/workflows/ci.yml/badge.svg)
![Platform](https://img.shields.io/badge/platform-Minikube%20%7C%20AWS-orange?style=flat&logo=amazon-aws)
![License](https://img.shields.io/badge/license-MIT-green?style=flat)

A production-grade, highly optimized Layer-7 Reverse Proxy and API Gateway written in pure Go. Routes traffic across multiple upstreams with automatic failure handling, token-bucket rate limiting, multi-stage container isolation, and a real-time asynchronous dual-telemetry engine (Prometheus Pull + AWS CloudWatch Push).

---

## Quick Start

### 1. Local Bare-Metal Testing (The Inner Loop)
```bash
# Clone and build
git clone [https://github.com/krishjj8/go-reverse-proxy](https://github.com/krishjj8/go-reverse-proxy)
cd go-reverse-proxy
go build -o proxy ./cmd/proxy/...

# Start two dummy upstreams (in separate terminals)
python3 -m http.server 8001
python3 -m http.server 8002

# Run the proxy locally
./proxy --config config.yaml

# Test routing
curl -H "Host: api.proxy" http://localhost:8080/

```

### 2. Local Kubernetes Orchestration (Minikube Verification)

```bash
# Build the highly optimized multi-stage image
docker build -t go-reverse-proxy:latest .

# Boot the local cluster and load the host container cache
minikube start --driver=docker
minikube image load go-reverse-proxy:latest

# Deploy the decoupled cluster resources
kubectl apply -f k8s/configmap.yaml
kubectl apply -f k8s/deployment.yaml

# Establish the local-to-cluster secure network tunnel
kubectl port-forward svc/go-reverse-proxy-service 8080:8080 9090:9090

# Test the administrative control plane health probe
curl -i http://localhost:9090/healthz

# Test the data plane rate limiter burst boundaries (watch for 429s)
for i in {1..30}; do
  curl -s -o /dev/null -w "%{http_code}\n" -H "Host: api.proxy" http://localhost:8080/
done

```

---

## Architecture

```
                       Client Inbound Request
                                  │
                                  ▼
                    ┌───────────────────────────┐
                    │    DATA PLANE (Port 8080) │
                    └─────────────┬─────────────┘
                                  │
                                  ▼
                    ┌───────────────────────────┐
                    │       Rate Limiter        │
                    │   (Token Bucket per IP)   │
                    └─────────────┬─────────────┘
                                  │
                                  ▼
                    ┌───────────────────────────┐
                    │      Circuit Breaker      │
                    │   Closed ⇆ Open ⇆ Half    │
                    └─────────────┬─────────────┘
                                  │
                                  ▼
                    ┌───────────────────────────┐
                    │ Round-Robin Load Balancer │
                    │ [target-1] [target-2] ... │
                    └─────────────┬─────────────┘
                                  │
                                  ▼
                    ┌───────────────────────────┐
                    │ Tuned HTTP Transport Pool │
                    │   HTTP Keep-Alive Reuse   │
                    └─────────────┬─────────────┘
                                  │
             ┌────────────────────┴────────────────────┐
             ▼                                         ▼
   Upstream Target A                         Upstream Target B
             │                                         │
             └────────────────────┬────────────────────┘
                                  │ (Async Channel Enqueue)
                                  ▼
                    ┌───────────────────────────┐
                    │  Async Telemetry Engine   │
                    └─────────────┬─────────────┘
                                  │
             ┌────────────────────┴────────────────────┐
             ▼                                         ▼
 CONTROL PLANE (Port 9090)               OUTER LOOP (Cloud)
 ├── /healthz (Orchestration Probe)       └── AWS CloudWatch Pipeline
 └── /metrics (Prometheus RAM Vectors)           (Active SDK Push)

```

---

## Features

### Round-Robin Load Balancing

Distributes requests across multiple upstream servers using a thread-safe upstream pool protected by `sync.RWMutex`. The design allows many concurrent readers while cluster health checks or backend status modifications safely run in the background.

### Circuit Breaker Protection

Implements a three-state finite state machine to prevent cascading application crashes:

* **Closed** — Normal operation, all incoming traffic forwarded cleanly.
* **Open** — Upstream marked unhealthy after 3 consecutive failures; traffic path is immediately short-circuited at the perimeter to protect system memory resources.
* **Half-Open** — After a 10-second cooldown window, a single canary request is let through to determine whether the target backend has fully recovered.

### Per-IP Rate Limiting

Uses an in-memory, thread-safe token-bucket implementation powered by `golang.org/x/time/rate`.

| Parameter | Value |
| --- | --- |
| Refill Rate | 10 requests/second |
| Burst Capacity | 20 requests |

Exceeding these configured thresholds drops connection paths at the perimeter middleware and returns an `HTTP 429 Too Many Requests`.

### Connection Pool Optimization

The HTTP transport layout is heavily tuned to minimize connection setup overhead under sustained traffic:

```go
MaxIdleConnsPerHost = 100

```

Reusing persistent TCP keep-alive connections cuts down handshake processing latency and improves raw system throughput.

### Asynchronous Dual-Telemetry Pipeline

Request handlers never block on external logging blocks or slow network API calls. Transaction data is dropped into an internal buffered channel in nanoseconds. A dedicated background consumer worker drains the queue and handles two streams:

1. **Prometheus Vectors:** Updates thread-safe counters and histogram metrics resident in local RAM.
2. **AWS CloudWatch:** Dispatches structured JSON objects and metrics updates to the AWS SDK client.

---

## What I Learned

Building this reverse proxy significantly deepened my understanding of backend systems, concurrency, networking, and cloud-native architecture.

**Synchronization primitives and scalability** — A standard `sync.Mutex` serializes all access and quickly becomes a bottleneck at scale. Migrating the upstream pool to `sync.RWMutex` allows thousands of concurrent reads while still protecting writes from the health-check goroutine. This tradeoff only becomes visible under load, which reinforced the value of thinking about access patterns before reaching for the simplest lock.

**Circuit breaker resilience** — The Half-Open state is easy to skip in a naive implementation, but it's the most important part. Without it, a recovered backend either never gets traffic back (stuck Open) or gets slammed immediately (no protection). Implementing the canary request pattern made the failure modes concrete and testable.

**Git internals in production** — Accidentally committing Terraform provider binaries that exceeded GitHub's file size limits forced me to rewrite the repository history with `git filter-branch`. That experience built intuition for how Git's object model actually works — commits, trees, and blobs — rather than just the surface-level commands.

**CI/CD on real infrastructure** — Debugging SCP permission failures in GitHub Actions taught me that non-root users, sudo boundaries, and file system ownership interact in subtle ways that local testing never surfaces. The fix was simple once understood; getting there required reading systemd logs, not just the Actions output.

**Async observability as a design constraint** — Seeing metrics arrive in CloudWatch while the proxy stayed responsive under load made the producer-consumer pattern click. Decoupling observation from execution isn't just a performance optimization — it's a correctness guarantee that the hot path stays predictable.

**Week 5 Isolation Mechanics** — Adding Prometheus metric vectors made me realize that vector coordinates do not explicitly initialize in the scraped text map layout on boot. They only materialize in memory once the first packet triggers the channel consumer loop and calls `.WithLabelValues()`. This optimizes RAM allocation within the cluster framework.

---

## Architectural Decisions

### Why Token Bucket Instead of Fixed Window Rate Limiting?

Fixed-window limiters can accidentally allow large traffic spikes at window boundaries (the "thundering herd at reset" problem). Token bucket algorithms smooth traffic naturally while still permitting controlled bursts, making them a better fit for production APIs where fairness matters more than simplicity.

### Why Use Buffered Channels for Telemetry?

Writing logs or publishing metrics directly inside the request path increases tail latency and creates tight coupling between the proxy's hot path and the availability of CloudWatch. The producer-consumer pattern separates concerns: the request handler stays lean, and the telemetry worker can retry, batch, or drop metrics independently.

### Why Use RWMutex Instead of Mutex?

The upstream list is read on every request and modified only during health checks. A standard mutex would serialize all access even when no write is occurring. `sync.RWMutex` allows arbitrarily many concurrent readers with no contention, while still guaranteeing exclusive access during the infrequent writes from the health-check goroutine.

### Why Remove Hop-by-Hop Headers?

Headers such as `Connection`, `Keep-Alive`, `Transfer-Encoding`, and `Upgrade` are defined as connection-specific by the HTTP/1.1 specification (RFC 7230) and must not be forwarded by intermediaries. Forwarding them causes protocol violations, mismanaged keep-alive state, and hard-to-debug connection errors at the upstream.

### Why Use Google Distroless Runtimes?

By swapping out heavy standard images for a minimal `gcr.io/distroless/static` baseline, the production footprint drops to just ~25MB. More importantly, it strips away full system shells (`/bin/sh`, `/bin/bash`) and package managers (`apk`, `apt`), meaning an attacker cannot execute local system commands or pull down external shell scripts even if a code exploit occurs.

---

## Installation

```bash
# Clone
git clone [https://github.com/krishjj8/go-reverse-proxy](https://github.com/krishjj8/go-reverse-proxy)
cd go-reverse-proxy

# Install and synchronize dependencies
go get [github.com/prometheus/client_golang/prometheus](https://github.com/prometheus/client_golang/prometheus)
go mod download
go mod verify

# Compile native binary via Docker multi-stage environment
docker build -t go-reverse-proxy:latest .

```

---

## Configuration

Edit `config.yaml` to define your listen address and upstream targets:

```yaml
server:
  listen_address: ":8080"

routes:
  api.proxy:
    upstreams:
      - "http://payment-svc:8001"
      - "http://inventory-svc:8002"

```

Route matching is done via the incoming `Host` header. Under Kubernetes orchestration, these addresses target the stable internal cluster CoreDNS virtual endpoints.

---

## Infrastructure & Environment

### Kubernetes Environment (Local Developer Inner Loop)

* **Local Runner:** Minikube virtualized runtime using the host Docker engine driver.
* **Decoupled Architecture:** Configuration states are completely abstracted away from immutable image layers. The cluster `ConfigMap` parameter rules are dynamically injected into the running pod's filesystem via storage volume definitions at boot time.

### Cloud Platform (Production Outer Loop)

* AWS EC2 (t2.micro)
* Ubuntu 24.04 LTS
* Deployed behind a systemd service (`go-proxy.service`)
* Custom VPC with public subnets, Internet Gateways, and security group rule allocations clamping incoming ingress strictly to port `8080`.

### Cloud Security

No hardcoded AWS access keys or secret blocks exist in the source or configuration code. CloudWatch `PutMetricData` actions are handled via temporary tokens provisioned through an IAM Instance Profile attached directly to the hosting instance compute layer (IMDSv2 standard).

---

## Observability

The proxy splits its monitoring profile across a public Data Plane and a private Control Plane to isolate resource contention.

Custom metrics tracked and exposed via port `9090/metrics`:

| Metric Name | Type | Label Dimensions | Purpose |
| --- | --- | --- | --- |
| `proxy_requests_total` | Counter | `upstream`, `status_code` | Monitors real-time transaction volume and system error budgets. |
| `proxy_request_duration_ms` | Histogram | `upstream` | Profiles latency bucket distributions to map p99 performance curves. |

Because all monitoring data is processed asynchronously via memory-resident loops, metric collection adds near-zero overhead to hot request paths.

---

## CI/CD Pipeline

Every code update pushed to the `main` branch fires an automated GitHub Actions testing and compilation verification matrix:

| Stage | Command / Job | Purpose |
| --- | --- | --- |
| Toolchain Init | `actions/setup-go@v5` | Boots an isolated runner using **Go 1.26** to match modules requirements. |
| Dependency Verify | `go mod verify` | Validates cryptographic checksum footprints of external modules. |
| Format Check | `gofmt -l .` | Rejects the workflow run if code changes violate Go syntax patterns. |
| Cross-Compile | `go build ./cmd/proxy/...` | Verifies full compiler approval by producing a static Linux binary. |
| Cloud Deploy | `if: false` (Paused) | Skipped during local Minikube testing intervals to safely prevent AWS billing accumulators. |

---

## Deployment Debugging Log

Real issues hit during development, documented for reference.

### SSH Key Permission Fix

Linux refuses SSH keys with world-readable permissions:

```bash
mv ~/Downloads/demo.pem ~/.ssh/demo.pem
chmod 400 ~/.ssh/demo.pem
ssh -i ~/.ssh/demo.pem ubuntu@<your-ec2-public-ip>

```

### Removing Terraform Provider Binaries from Git History

Large Terraform provider files exceeded GitHub's 100MB file size limit and persisted in commit history even after `.gitignore` was updated.

First, extend `.gitignore`:

```bash
cat >> .gitignore << 'EOF'
**/.terraform/
*.tfstate
*.tfstate.backup
*.tfvars
.terraform.lock.hcl
EOF

```

Then rewrite history to remove the files:

```bash
git filter-branch --force --index-filter \
  "git rm -rf --cached --ignore-unmatch infra/.terraform/" \
  --prune-empty --tag-name-filter cat -- --all

git push origin main --force

```

### Fixing SCP Deployment Permissions

Direct uploads to `/opt` failed because GitHub Actions connects as the non-root `ubuntu` user:

```bash
# Upload to home directory first
scp -i key proxy-binary ubuntu@<host>:~/proxy-binary

# Then move with sudo on the instance
sudo mkdir -p /opt/proxy
sudo mv ~/proxy-binary /opt/proxy/proxy
sudo chmod +x /opt/proxy/proxy

```

### Toolchain Version Mismatches

When updating dependencies for Prometheus integration, local module setups required Go 1.26 features while Docker base image environments targeted a Go 1.24 container runner. This triggered compiler safety terminations during the `go mod download` lifecycle. The mismatch was resolved by upgrading the `Dockerfile` builder stage target reference directly to `golang:1.26-alpine`.

---

## Validation Commands

```bash
# Verify the Administrative Control Plane Health Loop
curl -i http://localhost:9090/healthz

# Extract real-time memory-resident metrics allocations
curl http://localhost:9090/metrics

# Basic routing validation test via the local host tunnel
curl -H "Host: api.proxy" http://localhost:8080/

# Rate limiter load generation test — expect 20 successful responses followed by 429 drops
hey -n 50 -c 5 -H "Host: api.proxy" http://localhost:8080/

```

---

## Project Structure

```
go-reverse-proxy/
├── cmd/proxy/          # Entrypoint (Main loop initialization)
├── internal/proxy/     # Core engine logic (rate limiter, circuit breaker, admin control plane, telemetry)
├── internal/config/    # YAML configuration subsystem decoder logic
├── internal/logger/    # Structured machine-readable JSON logging config
├── k8s/                # Kubernetes Orchestration (ConfigMaps, HA Deployments, LoadBalancer Services)
├── Dockerfile          # Secure, multi-stage optimized Distroless compilation script
├── config.yaml         # Local bare-metal routing rules matrix parameter file
└── go.mod              # Module dependencies and toolchain safety maps

```

```
