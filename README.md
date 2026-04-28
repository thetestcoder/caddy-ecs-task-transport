# caddy-ecs-task-transport

A Caddy v2 HTTP handler plugin that dynamically routes wildcard preview subdomain requests to ECS task IPs resolved from PostgreSQL.

## How it works

```
Route53  →  ALB (443, ACM wildcard)  →  Caddy preview_router (ECS, N tasks)
                                              │
                                  ┌───────────┴───────────┐
                                  ▼                       ▼
                            PostgreSQL             ECS preview tasks
                         (hostname lookup)           (IP:5173)
```

1. A request arrives at `https://<previewId>.preview.example.com`.
2. The plugin validates the hostname suffix, then looks up the `hostname` in
   PostgreSQL table `vite_studio_domain_mappings` (with an in-memory TTL cache
   and singleflight deduplication).
3. The `task_ip` and `preview_port` from the `raw_response` JSONB column are
   used to reverse-proxy the request to `http://<task_ip>:<port>`.

## Requirements

| Tool     | Version              |
|----------|----------------------|
| Go       | 1.22+ (match your target Caddy release) |
| xcaddy   | latest (`go install github.com/caddyserver/xcaddy/cmd/xcaddy@latest`) |

## Build

```bash
# Local development (replace path with your clone location)
xcaddy build v2.9.1 \
  --with github.com/thetestcoder/caddy-ecs-task-transport=/path/to/caddy-ecs-task-transport

# From a published module
xcaddy build v2.9.1 \
  --with github.com/thetestcoder/caddy-ecs-task-transport@latest
```

Before building, resolve indirect Go module dependencies:

```bash
cd /path/to/caddy-ecs-task-transport
go mod tidy
```

Run the custom binary:

```bash
./caddy run --config Caddyfile
```

## Configuration reference

All options are available as Caddyfile subdirectives and JSON fields.

| Directive | JSON field | Description | Default |
|-----------|-----------|-------------|---------|
| `db_dsn` | `db_dsn` | PostgreSQL connection string. **Required.** Written directly in the Caddyfile. | — |
| `db_table` | `db_table` | Table name for the mapping lookup. | `vite_studio_domain_mappings` |
| `db_response_column` | `db_response_column` | Column containing the JSONB upstream data. | `raw_response` |
| `db_hostname_column` | `db_hostname_column` | Column to match the request hostname against. | `hostname` |
| `db_status_column` | `db_status_column` | Column used to filter active mappings. | `status` |
| `db_status_value` | `db_status_value` | Value the status column must equal. | `live` |
| `cache_ttl` | `cache_ttl` | How long hostname→upstream mappings are cached per Caddy task. | `5m` |
| `default_port` | `default_port` | Fallback port when `preview_port` is missing from the DB record. | `5173` |
| `allowed_domain_suffix` | `allowed_domain_suffix` | Only hostnames ending with this suffix are accepted. Include the leading dot. | `.preview.example.com` |
| `db_query_timeout` | `db_query_timeout` | Timeout for each database lookup query. | `2s` |
| `upstream_timeout` | `upstream_timeout` | Timeout waiting for upstream response headers. | `30s` |
| `health_path` | `health_path` | Path that returns `{"status":"ok"}` without touching the DB. | `/__health` |

## Example Caddyfile

**WARNING: The Caddyfile contains database credentials. Do not commit it to
version control. It is already listed in `.gitignore`.**

```caddyfile
{
    # Place the directive before reverse_proxy in the handler chain
    order preview_router before reverse_proxy
}

*.preview.example.com {
    encode zstd gzip

    preview_router {
        db_dsn                "postgres://user:password@rds-host:5432/dbname?sslmode=require"
        db_table              vite_studio_domain_mappings
        db_response_column    raw_response
        db_hostname_column    hostname
        db_status_column      status
        db_status_value       live
        cache_ttl             5m
        default_port          5173
        allowed_domain_suffix .preview.example.com
    }
}
```

If you prefer explicit ordering, wrap in a `route` block instead of using `order`:

```caddyfile
*.preview.example.com {
    route {
        preview_router {
            db_dsn "postgres://user:password@rds-host:5432/dbname?sslmode=require"
        }
    }
}
```

## Built-in endpoints

| Path | Method | Description |
|------|--------|-------------|
| `/__health` | GET | Returns `200 {"status":"ok"}`. No DB call. Suitable for ALB target group health checks. |
| `/__metrics` | GET | Returns JSON counters: `requests`, `cache_hits`, `cache_misses`, `db_errors`, `proxy_errors`. |

## HTTP status codes

| Code | Meaning |
|------|---------|
| 403 | Hostname does not match `allowed_domain_suffix`. |
| 404 | No live mapping found for the hostname in PostgreSQL. |
| 500 | Database or JSON parsing error. |
| 502 | `task_ip` is empty in the mapping, or the upstream ECS task is unreachable. |

## Deployment

See [docs/deployment-ec2.md](docs/deployment-ec2.md) for a full production runbook covering:

- Route53 → ALB (ACM wildcard) → Caddy ECS service → preview tasks
- Security groups, Caddyfile credential handling, DNS/TLS
- Bare EC2 with systemd (example unit in [docs/caddy.service.example](docs/caddy.service.example))
- Production Caddyfile template in [docs/Caddyfile.ec2.example](docs/Caddyfile.ec2.example)

## Architecture notes

- **Cache is per-task.** Each Caddy ECS replica maintains its own in-memory
  cache. There is no cross-task invalidation. Steady-state DB load is low
  thanks to the 5-minute default TTL and singleflight deduplication.
- **Shared transport.** A single `http.Transport` and `httputil.ReverseProxy`
  are allocated once during `Provision()` and reused for all requests. No
  per-request allocation.
- **Streaming.** `FlushInterval: -1` ensures SSE and Vite HMR streams are
  flushed to the client immediately.

## Troubleshooting

**404 on a hostname you just created** — The mapping may not yet have
`status = 'live'` in the database, or the previous negative result is still
cached. Wait up to `cache_ttl` (default 5 min) or lower the TTL.

**502 Bad Gateway** — Either `task_ip` is empty in the DB record, or the ECS
preview task is not reachable on the expected port. Check security groups
(Caddy task SG → preview task SG on port 5173).

**500 Internal Server Error** — Check Caddy logs for the underlying DB or JSON
parse error. Verify the `db_dsn` value in your Caddyfile is correct and that
`raw_response` contains valid JSON with `task_ip`.

## License

MIT
