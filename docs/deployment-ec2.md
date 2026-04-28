# Deployment guide

This guide covers deploying the `preview_router` Caddy plugin in two scenarios:

1. **ECS behind ALB** (recommended for production)
2. **Bare EC2 with systemd**

---

## Reference architecture

```
Route53 (*.preview.example.com → ALB alias)
   │
   ▼
ALB  (listener 443, ACM wildcard cert *.preview.example.com)
   │
   ▼
ECS service: caddy-preview-router  (N tasks, HTTP on container port 8080)
   │                        │
   │  cache miss            │  reverse proxy
   ▼                        ▼
PostgreSQL / RDS            ECS preview tasks (private IP:5173)
```

### Security groups

| Source | Destination | Port | Purpose |
|--------|------------|------|---------|
| Internet / CloudFront | ALB | 443 | Public HTTPS |
| ALB SG | Caddy service SG | 8080 | ALB → Caddy |
| Caddy service SG | RDS SG | 5432 | Caddy → PostgreSQL |
| Caddy service SG | Preview task SG | 5173 | Caddy → preview upstream |

---

## 1. ECS behind ALB

### Build the custom Caddy image

Create a multi-stage Dockerfile in the repository root:

```dockerfile
FROM golang:1.22 AS builder
RUN go install github.com/caddyserver/xcaddy/cmd/xcaddy@latest
WORKDIR /src
COPY . .
RUN go mod tidy
RUN xcaddy build v2.9.1 \
      --with github.com/thetestcoder/caddy-ecs-task-transport=. \
      --output /usr/local/bin/caddy

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates && rm -rf /var/lib/apt/lists/*
COPY --from=builder /usr/local/bin/caddy /usr/local/bin/caddy
COPY docs/Caddyfile.ec2.example /etc/caddy/Caddyfile
EXPOSE 8080
CMD ["caddy", "run", "--config", "/etc/caddy/Caddyfile"]
```

Push to ECR:

```bash
docker build -t caddy-preview-router .
docker tag caddy-preview-router:latest <account>.dkr.ecr.<region>.amazonaws.com/caddy-preview-router:latest
docker push <account>.dkr.ecr.<region>.amazonaws.com/caddy-preview-router:latest
```

### ECS task definition (key fields)

The `db_dsn` credential is written directly in the Caddyfile that is baked
into the container image (or mounted at runtime). **Do not store the Caddyfile
in version control.**

If you prefer to keep credentials out of the image, mount the Caddyfile from
an S3 object, EFS volume, or an init container that writes it at startup from
Secrets Manager / SSM.

```json
{
  "containerDefinitions": [
    {
      "name": "caddy",
      "image": "<account>.dkr.ecr.<region>.amazonaws.com/caddy-preview-router:latest",
      "portMappings": [{ "containerPort": 8080, "protocol": "tcp" }],
      "environment": [],
      "logConfiguration": {
        "logDriver": "awslogs",
        "options": {
          "awslogs-group": "/ecs/caddy-preview-router",
          "awslogs-region": "<region>",
          "awslogs-stream-prefix": "caddy"
        }
      }
    }
  ],
  "cpu": "512",
  "memory": "1024",
  "networkMode": "awsvpc"
}
```

### ALB target group

- **Protocol**: HTTP
- **Port**: 8080
- **Health check path**: `/__health`
- **Health check interval**: 30s
- **Healthy threshold**: 2
- **Unhealthy threshold**: 3

### ALB listener

- **Port**: 443
- **Certificate**: ACM wildcard for `*.preview.example.com`
- **Default action**: forward to the target group above

### DNS

Create a Route53 alias record:

- **Name**: `*.preview.example.com`
- **Type**: A (alias)
- **Target**: the ALB DNS name

### Caddy behind ALB (TLS termination)

Since TLS terminates at the ALB, Caddy listens on plain HTTP. The Caddyfile
must **not** enable auto-HTTPS for this site. Use `http://` explicitly or
set `auto_https off` in global options.

ALB forwards `X-Forwarded-For`, `X-Forwarded-Proto`, and preserves the
original `Host` header by default. The plugin reads `r.Host` directly, so no
extra Caddy `trusted_proxies` config is needed as long as your ALB preserves
Host (the default).

If you use a non-default ALB routing rule that rewrites Host, configure
`servers > trusted_proxies` in Caddy's JSON config.

### Caching at scale

Each Caddy ECS task maintains its own in-memory hostname→upstream cache with a
default TTL of **5 minutes**. When a mapping changes in the database, the
worst-case propagation delay is `cache_ttl × 1` (per replica, independently).

Steady-state DB traffic: approximately `unique_hostnames / cache_ttl` queries
per task. With 100 active hostnames, 5 min TTL, and 3 tasks, expect
~1 query/sec total to the database.

Lower `cache_ttl` for faster propagation at the cost of higher DB traffic.

---

## 2. Bare EC2 with systemd

### Install Go and xcaddy

```bash
# Amazon Linux 2023 / Ubuntu 22.04+
sudo dnf install -y golang   # or: sudo apt install -y golang-go

go install github.com/caddyserver/xcaddy/cmd/xcaddy@latest
```

### Build

```bash
git clone https://github.com/thetestcoder/caddy-ecs-task-transport.git
cd caddy-ecs-task-transport
go mod tidy

xcaddy build v2.9.1 \
  --with github.com/thetestcoder/caddy-ecs-task-transport=. \
  --output ./caddy

# Install
sudo mv caddy /usr/local/bin/caddy
sudo chmod +x /usr/local/bin/caddy
```

### Non-root port binding

If Caddy needs to bind ports 80/443 without running as root:

```bash
sudo setcap 'cap_net_bind_service=+ep' /usr/local/bin/caddy
```

When running behind an ALB, Caddy binds a high port (e.g. 8080) and this step
is not needed.

### Caddyfile

Copy the example and edit:

```bash
sudo mkdir -p /etc/caddy
sudo cp docs/Caddyfile.ec2.example /etc/caddy/Caddyfile
sudo vim /etc/caddy/Caddyfile
```

### Credentials

The `db_dsn` is written directly in `/etc/caddy/Caddyfile`. Protect the file:

```bash
sudo chown caddy:caddy /etc/caddy/Caddyfile
sudo chmod 600 /etc/caddy/Caddyfile
```

**Never commit the Caddyfile to version control — it contains the database
connection string with credentials.**

### systemd service

```bash
sudo cp docs/caddy.service.example /etc/systemd/system/caddy.service
sudo systemctl daemon-reload
sudo systemctl enable --now caddy
```

Check status:

```bash
sudo systemctl status caddy
sudo journalctl -u caddy -f
```

### Upgrades

1. Build a new binary with `xcaddy build`.
2. Replace `/usr/local/bin/caddy`.
3. `sudo systemctl restart caddy`.

For zero-downtime reloads, use `caddy reload --config /etc/caddy/Caddyfile`
(applies config without restarting the process).

---

## Connection pool tuning

The plugin creates a PostgreSQL connection pool with these defaults:

| Setting | Value | Notes |
|---------|-------|-------|
| `MaxConns` | 20 | Per Caddy task. Total across N tasks = 20×N. Must not exceed RDS `max_connections`. |
| `MinConns` | 2 | Kept warm for low-latency cache misses. |
| `MaxConnLifetime` | 30 min | Recycles connections to handle DNS/failover changes. |
| `MaxConnIdleTime` | 5 min | Closes idle connections to free DB resources. |

If you run many Caddy tasks, consider using **RDS Proxy** to multiplex
connections and avoid hitting the RDS connection limit.
