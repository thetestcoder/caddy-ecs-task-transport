package previewrouter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
	"golang.org/x/sync/singleflight"
)

var (
	errNotFound = errors.New("no live mapping found")
	errNoTaskIP = errors.New("task_ip is empty in mapping")
)

type upstreamTarget struct {
	IP   string
	Port int
}

type upstreamCtxKey struct{}

type routerMetrics struct {
	requests    atomic.Int64
	cacheHits   atomic.Int64
	cacheMisses atomic.Int64
	dbErrors    atomic.Int64
	proxyErrors atomic.Int64
}

// PreviewRouter is a Caddy HTTP handler that resolves preview hostnames
// to ECS task IPs via PostgreSQL and reverse-proxies the request.
type PreviewRouter struct {
	// PostgreSQL connection string.
	DBDSN string `json:"db_dsn"`

	// How long resolved hostname→upstream mappings are cached in memory.
	CacheTTL caddy.Duration `json:"cache_ttl,omitempty"`

	// Fallback port when preview_port is absent from the DB record.
	DefaultPort int `json:"default_port,omitempty"`

	// Only hostnames ending with this suffix are accepted.
	AllowedDomainSuffix string `json:"allowed_domain_suffix,omitempty"`

	// Timeout for each DB lookup query.
	DBQueryTimeout caddy.Duration `json:"db_query_timeout,omitempty"`

	// Timeout waiting for the upstream response headers.
	UpstreamTimeout caddy.Duration `json:"upstream_timeout,omitempty"`

	// Path that returns a lightweight health response without touching DB.
	HealthPath string `json:"health_path,omitempty"`

	logger        *zap.Logger
	pool          *pgxpool.Pool
	cache         *hostCache
	proxy         *httputil.ReverseProxy
	transport     *http.Transport
	sfGroup       singleflight.Group
	ctx           context.Context
	cancel        context.CancelFunc
	metrics       *routerMetrics
	allowedSuffix string
}

// Interface guards.
var (
	_ caddy.Module                = (*PreviewRouter)(nil)
	_ caddy.Provisioner           = (*PreviewRouter)(nil)
	_ caddy.Validator             = (*PreviewRouter)(nil)
	_ caddy.CleanerUpper          = (*PreviewRouter)(nil)
	_ caddyhttp.MiddlewareHandler = (*PreviewRouter)(nil)
)

func (h *PreviewRouter) Provision(ctx caddy.Context) error {
	h.logger = ctx.Logger()
	h.metrics = &routerMetrics{}
	h.ctx, h.cancel = context.WithCancel(context.Background())

	if h.DBDSN == "" {
		return fmt.Errorf("preview_router: db_dsn is required")
	}

	// Defaults
	if h.CacheTTL == 0 {
		h.CacheTTL = caddy.Duration(5 * time.Minute)
	}
	if h.DefaultPort == 0 {
		h.DefaultPort = 5173
	}
	if h.AllowedDomainSuffix == "" {
		h.AllowedDomainSuffix = ".preview.example.com"
	}
	if h.DBQueryTimeout == 0 {
		h.DBQueryTimeout = caddy.Duration(2 * time.Second)
	}
	if h.UpstreamTimeout == 0 {
		h.UpstreamTimeout = caddy.Duration(30 * time.Second)
	}
	if h.HealthPath == "" {
		h.HealthPath = "/__health"
	}

	h.allowedSuffix = strings.ToLower(strings.TrimSpace(h.AllowedDomainSuffix))

	// TTL cache
	h.cache = newHostCache(time.Duration(h.CacheTTL))

	// PostgreSQL connection pool
	h.logger.Info("connecting to database",
		zap.String("host", redactDSN(h.DBDSN)),
		zap.Int32("max_conns", 20),
		zap.Int32("min_conns", 2),
		zap.Duration("max_conn_lifetime", 30*time.Minute),
		zap.Duration("max_conn_idle_time", 5*time.Minute),
	)

	poolCfg, err := pgxpool.ParseConfig(h.DBDSN)
	if err != nil {
		h.logger.Error("failed to parse db_dsn", zap.Error(err))
		return fmt.Errorf("preview_router: parsing db_dsn: %w", err)
	}
	poolCfg.MaxConns = 20
	poolCfg.MinConns = 2
	poolCfg.MaxConnLifetime = 30 * time.Minute
	poolCfg.MaxConnIdleTime = 5 * time.Minute

	h.pool, err = pgxpool.NewWithConfig(h.ctx, poolCfg)
	if err != nil {
		h.logger.Error("failed to create database pool", zap.Error(err))
		return fmt.Errorf("preview_router: creating db pool: %w", err)
	}
	h.logger.Info("database pool created, pinging...")

	if err := h.pool.Ping(h.ctx); err != nil {
		h.logger.Error("database ping failed", zap.Error(err))
		h.pool.Close()
		return fmt.Errorf("preview_router: db ping failed: %w", err)
	}
	h.logger.Info("database connection verified")

	// Shared reverse proxy + transport (no response caching)
	h.setupProxy()

	h.logger.Info("preview_router provisioned",
		zap.String("allowed_suffix", h.allowedSuffix),
		zap.Duration("cache_ttl", time.Duration(h.CacheTTL)),
		zap.Int("default_port", h.DefaultPort),
		zap.String("health_path", h.HealthPath),
		zap.Duration("db_query_timeout", time.Duration(h.DBQueryTimeout)),
		zap.Duration("upstream_timeout", time.Duration(h.UpstreamTimeout)),
	)
	return nil
}

func (h *PreviewRouter) Validate() error {
	if h.DBDSN == "" {
		return fmt.Errorf("preview_router: db_dsn is required")
	}
	if h.AllowedDomainSuffix == "" {
		return fmt.Errorf("preview_router: allowed_domain_suffix is required")
	}
	return nil
}

func (h *PreviewRouter) Cleanup() error {
	if h.cancel != nil {
		h.cancel()
	}
	if h.cache != nil {
		h.cache.Stop()
	}
	if h.pool != nil {
		h.pool.Close()
	}
	if h.transport != nil {
		h.transport.CloseIdleConnections()
	}
	if h.logger != nil {
		h.logger.Info("preview_router cleaned up")
	}
	return nil
}

func (h *PreviewRouter) ServeHTTP(w http.ResponseWriter, r *http.Request, next caddyhttp.Handler) error {
	start := time.Now()
	h.metrics.requests.Add(1)

	// Health check — no DB, no cache.
	if h.HealthPath != "" && r.URL.Path == h.HealthPath {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
		return nil
	}

	// Metrics endpoint.
	if r.URL.Path == "/__metrics" {
		h.writeMetrics(w)
		return nil
	}

	// Validate hostname suffix.
	hostname := strings.ToLower(extractHost(r.Host))
	h.logger.Debug("incoming request",
		zap.String("hostname", hostname),
		zap.String("method", r.Method),
		zap.String("path", r.URL.Path),
		zap.String("remote_addr", r.RemoteAddr),
	)

	if !strings.HasSuffix(hostname, h.allowedSuffix) {
		h.logger.Warn("rejected hostname: suffix not allowed",
			zap.String("hostname", hostname),
			zap.String("allowed_suffix", h.allowedSuffix),
		)
		return caddyhttp.Error(http.StatusForbidden,
			fmt.Errorf("host %q is not permitted", hostname))
	}

	// Resolve upstream from cache or DB.
	target, err := h.resolveUpstream(r.Context(), hostname)
	if err != nil {
		if errors.Is(err, errNotFound) {
			h.logger.Warn("no live mapping found",
				zap.String("hostname", hostname))
			return caddyhttp.Error(http.StatusNotFound,
				fmt.Errorf("no live mapping for %s", hostname))
		}
		if errors.Is(err, errNoTaskIP) {
			h.logger.Warn("mapping has empty task_ip",
				zap.String("hostname", hostname))
			return caddyhttp.Error(http.StatusBadGateway,
				fmt.Errorf("upstream unavailable for %s", hostname))
		}
		h.logger.Error("failed to resolve upstream",
			zap.String("hostname", hostname), zap.Error(err))
		return caddyhttp.Error(http.StatusInternalServerError,
			fmt.Errorf("internal error resolving %s", hostname))
	}

	upstream := net.JoinHostPort(target.IP, strconv.Itoa(target.Port))
	h.logger.Info("proxying request",
		zap.String("hostname", hostname),
		zap.String("upstream", upstream),
		zap.String("method", r.Method),
		zap.String("path", r.URL.Path),
	)

	// Proxy to the resolved ECS task.
	proxyCtx := context.WithValue(r.Context(), upstreamCtxKey{}, target)
	h.proxy.ServeHTTP(w, r.WithContext(proxyCtx))

	h.logger.Debug("request completed",
		zap.String("hostname", hostname),
		zap.String("upstream", upstream),
		zap.Duration("latency", time.Since(start)),
	)
	return nil
}

// extractHost strips the port from a host:port string.
func extractHost(hostport string) string {
	host, _, err := net.SplitHostPort(hostport)
	if err != nil {
		return hostport
	}
	return host
}

// redactDSN returns the DSN host/db for logging without credentials.
func redactDSN(dsn string) string {
	at := strings.LastIndex(dsn, "@")
	if at == -1 {
		return "(no-auth)"
	}
	return dsn[at+1:]
}

func (h *PreviewRouter) writeMetrics(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]int64{
		"requests":     h.metrics.requests.Load(),
		"cache_hits":   h.metrics.cacheHits.Load(),
		"cache_misses": h.metrics.cacheMisses.Load(),
		"db_errors":    h.metrics.dbErrors.Load(),
		"proxy_errors": h.metrics.proxyErrors.Load(),
	})
}
