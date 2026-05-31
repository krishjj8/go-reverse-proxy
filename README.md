# Go Reverse Proxy Gateway

A production-grade reverse proxy written in pure Go. Routes traffic across multiple upstreams with automatic failure handling, token-bucket rate limiting, and real-time asynchronous AWS CloudWatch telemetry.

## Architecture

```text
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

## Features

### Round-Robin Load Balancing

Distributes requests across multiple upstream servers using a thread-safe upstream pool protected by `sync.RWMutex`. The design allows many concurrent readers while health checks safely update backend status in the background.

### Circuit Breaker Protection

Implements a three-state finite state machine:

* Closed
* Open
* Half-Open

After three consecutive failures from an upstream, the circuit transitions to Open and immediately rejects traffic to the unhealthy backend. Following a cooldown period, a Half-Open canary request determines whether the backend has recovered.

### Per-IP Rate Limiting

Uses a token-bucket implementation powered by `golang.org/x/time/rate`.

* Refill Rate: 10 requests/second
* Burst Capacity: 20 requests

This prevents abusive clients from overwhelming the proxy or downstream services.

### Connection Pool Optimization

The HTTP transport is configured with:

```go
MaxIdleConnsPerHost = 100
```

Persistent keep-alive connections reduce connection setup overhead and improve request efficiency under sustained traffic.

### Asynchronous Telemetry Pipeline

Request handlers never block on logging or cloud API calls.

Instead, telemetry events are pushed into a buffered Go channel and processed by a dedicated background worker, which publishes metrics and structured logs to AWS CloudWatch.

## Infrastructure & Environment

### Cloud Platform

* AWS EC2
* Ubuntu 24.04 LTS
* t2.micro deployment environment

### Networking

* Custom VPC
* Public subnet configuration
* Internet Gateway
* Route Table associations

### Security

The application does not use hardcoded AWS credentials.

Instead, telemetry access is granted through:

* IAM Role
* IAM Instance Profile
* CloudWatch permissions attached to the instance

This follows AWS security best practices by leveraging temporary credentials provided through instance metadata.

## Architectural Decisions

### Why Token Bucket Instead of Fixed Window Rate Limiting?

Fixed-window limiters can accidentally allow large traffic spikes at window boundaries.

Token bucket algorithms smooth traffic naturally while still allowing controlled bursts, making them a better fit for production APIs.

### Why Use Buffered Channels for Telemetry?

Writing logs or publishing metrics directly inside the request path increases latency and creates unnecessary coupling.

The producer-consumer pattern separates request handling from observability workloads, keeping the hot path lightweight.

### Why Use RWMutex Instead of Mutex?

The upstream list is read frequently and modified infrequently.

A standard mutex would serialize all access. Using `sync.RWMutex` allows many concurrent readers while still protecting writes performed by the health-check subsystem.

### Why Remove Hop-by-Hop Headers?

Headers such as:

* Connection
* Keep-Alive
* Transfer-Encoding
* Upgrade

must not be forwarded by proxies according to HTTP specifications. Removing them prevents protocol violations and connection management issues.

## Installation

### Clone Repository

```bash
git clone https://github.com/krishjj8/go-reverse-proxy

cd go-reverse-proxy
```

### Install Dependencies

```bash
go mod download

go mod verify
```

### Build

```bash
go build -o proxy ./cmd/proxy/...
```

## Configuration

Example `config.yaml`

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

## Deployment Notes & Debugging Log

This project encountered several real-world deployment issues that were solved during development.

### SSH Key Permission Fix

Linux refuses to use SSH keys with insecure permissions.

```bash
mv ~/Downloads/demo.pem ~/.ssh/demo.pem

chmod 400 ~/.ssh/demo.pem

ssh -i ~/.ssh/demo.pem ubuntu@3.111.144.151
```

### Removing Terraform Provider Binaries from Git History

Large Terraform provider files exceeded GitHub's file-size limits and remained in commit history even after being added to `.gitignore`.

```bash
cat >> .gitignore << 'EOF'
**/.terraform/
*.tfstate
*.tfstate.backup
*.tfvars
.terraform.lock.hcl
EOF
```

Rewrite repository history:

```bash
git filter-branch --force --index-filter \
"git rm -rf --cached --ignore-unmatch infra/.terraform/" \
--prune-empty --tag-name-filter cat -- --all

git push origin main --force
```

### Fixing SCP Deployment Permissions

Direct uploads to `/opt` failed because GitHub Actions connected as the non-root `ubuntu` user.

Working solution:

```bash
scp -i key proxy-binary ubuntu@host:~/proxy-binary

sudo mkdir -p /opt/proxy

sudo mv ~/proxy-binary /opt/proxy/proxy

sudo chmod +x /opt/proxy/proxy
```

## Validation Commands

### Basic Routing Test

```bash
curl -H "Host: api.proxy" \
http://3.111.144.151:8080/get
```

### Rate Limiter Test

```bash
for i in {1..30}; do
  curl -s -o /dev/null -w "%{http_code}\n" \
  -H "Host: api.proxy" \
  http://3.111.144.151:8080/get
done
```

Expected behavior:

* Initial requests return `200 OK`
* Burst traffic eventually receives `429 Too Many Requests`

## Observability & Validation

The proxy publishes custom metrics to AWS CloudWatch through an asynchronous telemetry pipeline built around a buffered Go channel.

During deployment testing, CloudWatch was used to verify:

* Request traffic reaching the gateway
* Upstream health state transitions
* Rate limiter activation events
* Circuit breaker trip and recovery behavior
* End-to-end latency trends

Because telemetry is processed asynchronously, metric collection does not block request execution.

### Future Benchmarking

Future performance testing will use tools such as:

* wrk
* vegeta
* k6

to establish measurable throughput, latency percentiles, and recovery benchmarks under sustained load.

## CI/CD Pipeline

Every push to the `main` branch triggers a GitHub Actions workflow.

Pipeline stages include:

1. Code formatting validation (`gofmt`)
2. Dependency verification (`go mod verify`)
3. Linux binary compilation (`GOOS=linux GOARCH=amd64`)
4. Secure deployment through SSH/SCP
5. Service restart using systemd

```bash
sudo systemctl restart go-proxy
```

## What I Learned

Building this reverse proxy significantly improved my understanding of backend systems, concurrency, networking, and cloud infrastructure.

One of the biggest lessons was learning how synchronization primitives affect scalability. A standard `sync.Mutex` quickly becomes a bottleneck when many requests compete for shared resources. Migrating the upstream pool to `sync.RWMutex` allowed thousands of concurrent reads while still maintaining safe updates from health checks.

Implementing the circuit breaker also reinforced the importance of resilience patterns. The Half-Open state proved essential because it provides a controlled recovery mechanism instead of immediately restoring full traffic to a previously unhealthy backend.

On the infrastructure side, solving deployment issues provided practical experience with production workflows. Rewriting Git history to remove accidentally committed Terraform provider binaries and debugging SCP permission failures helped build a deeper understanding of Git internals, Linux permissions, and CI/CD deployment pipelines.

Finally, seeing request metrics arrive in AWS CloudWatch through an asynchronous telemetry pipeline connected application design, observability, and cloud operations into a complete production-ready system.
