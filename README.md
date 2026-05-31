# Go Reverse Proxy Gateway

![Go](https://img.shields.io/badge/Go-1.21+-00ADD8?style=flat&logo=go&logoColor=white)
![CI](https://github.com/krishjj8/go-reverse-proxy/actions/workflows/deploy.yml/badge.svg)
![Platform](https://img.shields.io/badge/platform-AWS%20EC2-orange?style=flat&logo=amazon-aws)
![License](https://img.shields.io/badge/license-MIT-green?style=flat)

A production-grade reverse proxy written in pure Go. Routes traffic across multiple upstreams with automatic failure handling, token-bucket rate limiting, and real-time asynchronous AWS CloudWatch telemetry.

---

## Quick Start

```bash
# Clone and build
git clone https://github.com/krishjj8/go-reverse-proxy
cd go-reverse-proxy
go build -o proxy ./cmd/proxy/...

# Start two dummy upstreams (in separate terminals)
python3 -m http.server 8001
python3 -m http.server 8002

# Run the proxy
./proxy --config config.yaml

# Test routing
curl -H "Host: api.proxy" http://localhost:8080/

# Test rate limiting (watch for 429s)
for i in {1..30}; do
  curl -s -o /dev/null -w "%{http_code}\n" \
  -H "Host: api.proxy" http://localhost:8080/
done
```

---

## Architecture

```
Client Request
      │
      ▼
┌─────────────────────────────────┐
│         Rate Limiter            │
│    (token bucket per IP)        │
└────────────────┬────────────────┘
                 │
                 ▼
┌─────────────────────────────────┐
│       Circuit Breaker           │
│   Closed → Open → Half-Open     │
└────────────────┬────────────────┘
                 │
                 ▼
┌─────────────────────────────────┐
│     Round-Robin Load Balancer   │
│   [upstream-1][upstream-2]...   │
└────────────────┬────────────────┘
                 │
                 ▼
┌─────────────────────────────────┐
│    Tuned HTTP Transport Pool    │
│     HTTP Keep-Alive Reuse       │
└────────────────┬────────────────┘
                 │
                 ▼
             Upstream
                 │
                 ▼
┌─────────────────────────────────┐
│   Async Telemetry Channel       │
│   CloudWatch + Structured Logs  │
└─────────────────────────────────┘
```

---

## Features

### Round-Robin Load Balancing

Distributes requests across multiple upstream servers using a thread-safe upstream pool protected by `sync.RWMutex`. The design allows many concurrent readers while health checks safely update backend status in the background.

### Circuit Breaker Protection

Implements a three-state finite state machine:

- **Closed** — normal operation, all traffic forwarded
- **Open** — upstream marked unhealthy after 3 consecutive failures; traffic immediately rejected
- **Half-Open** — after cooldown, a single canary request determines whether the backend has recovered

### Per-IP Rate Limiting

Uses a token-bucket implementation powered by `golang.org/x/time/rate`.

| Parameter | Value |
|---|---|
| Refill Rate | 10 requests/second |
| Burst Capacity | 20 requests |

Exceeding limits returns `429 Too Many Requests`.

### Connection Pool Optimization

The HTTP transport is configured with:

```go
MaxIdleConnsPerHost = 100
```

Persistent keep-alive connections reduce connection setup overhead and improve throughput under sustained traffic.

### Asynchronous Telemetry Pipeline

Request handlers never block on logging or cloud API calls. Telemetry events are pushed into a buffered Go channel and processed by a dedicated background worker, which publishes metrics and structured logs to AWS CloudWatch.

---

## What I Learned

Building this reverse proxy significantly deepened my understanding of backend systems, concurrency, networking, and cloud infrastructure.

**Synchronization primitives and scalability** — A standard `sync.Mutex` serializes all access and quickly becomes a bottleneck at scale. Migrating the upstream pool to `sync.RWMutex` allows thousands of concurrent reads while still protecting writes from the health-check goroutine. This tradeoff only becomes visible under load, which reinforced the value of thinking about access patterns before reaching for the simplest lock.

**Circuit breaker resilience** — The Half-Open state is easy to skip in a naive implementation, but it's the most important part. Without it, a recovered backend either never gets traffic back (stuck Open) or gets slammed immediately (no protection). Implementing the canary request pattern made the failure modes concrete and testable.

**Git internals in production** — Accidentally committing Terraform provider binaries that exceeded GitHub's file size limits forced me to rewrite the repository history with `git filter-branch`. That experience built intuition for how Git's object model actually works — commits, trees, and blobs — rather than just the surface-level commands.

**CI/CD on real infrastructure** — Debugging SCP permission failures in GitHub Actions taught me that non-root users, sudo boundaries, and file system ownership interact in subtle ways that local testing never surfaces. The fix was simple once understood; getting there required reading systemd logs, not just the Actions output.

**Async observability as a design constraint** — Seeing metrics arrive in CloudWatch while the proxy stayed responsive under load made the producer-consumer pattern click. Decoupling observation from execution isn't just a performance optimization — it's a correctness guarantee that the hot path stays predictable.

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

---

## Installation

```bash
# Clone
git clone https://github.com/krishjj8/go-reverse-proxy
cd go-reverse-proxy

# Install dependencies
go mod download
go mod verify

# Build
go build -o proxy ./cmd/proxy/...
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
      - "http://127.0.0.1:8001"
      - "http://127.0.0.1:8002"
      - "http://httpbin.org"
```

Route matching is done via the `Host` header. Add additional keys under `routes` for multiple virtual hosts.

---

## Infrastructure & Environment

### Cloud Platform

- AWS EC2 (t2.micro)
- Ubuntu 24.04 LTS
- Deployed behind a systemd service (`go-proxy.service`)

### Networking

- Custom VPC with public subnet
- Internet Gateway + Route Table association
- Security group: port `8080` open for proxy traffic

### Security

No hardcoded AWS credentials. Telemetry access is granted through:

- IAM Role attached to the EC2 instance
- IAM Instance Profile
- CloudWatch `PutMetricData` permissions scoped to the role

This follows AWS best practices by using temporary credentials from instance metadata (IMDSv2) rather than static keys.

---

## Observability

The proxy publishes custom metrics to AWS CloudWatch through the async telemetry pipeline.

Metrics tracked during deployment validation:

| Signal | What it verified |
|---|---|
| Request count | Traffic reaching the gateway |
| Upstream health state | Circuit breaker transitions |
| Rate limiter events | 429 triggers under burst load |
| End-to-end latency | No telemetry overhead on hot path |

Because telemetry is processed asynchronously, metric collection does not block request execution.

---

## CI/CD Pipeline

Every push to `main` triggers a GitHub Actions workflow:

| Stage | Command |
|---|---|
| Format check | `gofmt -l .` |
| Dependency verify | `go mod verify` |
| Cross-compile | `GOOS=linux GOARCH=amd64 go build` |
| Deploy | SCP binary to EC2 |
| Restart | `sudo systemctl restart go-proxy` |

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

---

## Validation Commands

```bash
# Basic routing test
curl -H "Host: api.proxy" http://<your-ec2-public-ip>:8080/get

# Rate limiter test — expect 200s followed by 429s
for i in {1..30}; do
  curl -s -o /dev/null -w "%{http_code}\n" \
  -H "Host: api.proxy" \
  http://<your-ec2-public-ip>:8080/get
done
```

---

## Project Structure

```
go-reverse-proxy/
├── cmd/proxy/          # Entrypoint
├── internal/           # Core proxy logic (balancer, circuit breaker, rate limiter, telemetry)
├── infra/              # Terraform — VPC, EC2, IAM
├── .github/workflows/  # CI/CD pipeline
├── config.yaml         # Example configuration
└── main                # Compiled binary (gitignored in production)
```
