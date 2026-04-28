package previewrouter

import (
	"strconv"
	"time"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	"github.com/caddyserver/caddy/v2/caddyconfig/httpcaddyfile"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
)

func init() {
	httpcaddyfile.RegisterHandlerDirective("preview_router", parseCaddyfile)
}

func parseCaddyfile(h httpcaddyfile.Helper) (caddyhttp.MiddlewareHandler, error) {
	var m PreviewRouter
	err := m.UnmarshalCaddyfile(h.Dispenser)
	return &m, err
}

// UnmarshalCaddyfile sets up the handler from Caddyfile tokens.
//
//	preview_router {
//	    db_dsn                <dsn>
//	    db_table              <table>
//	    db_response_column    <column>
//	    db_hostname_column    <column>
//	    db_status_column      <column>
//	    db_status_value       <value>
//	    cache_ttl             <duration>
//	    default_port          <port>
//	    allowed_domain_suffix <suffix>
//	    db_query_timeout      <duration>
//	    upstream_timeout      <duration>
//	    health_path           <path>
//	}
func (h *PreviewRouter) UnmarshalCaddyfile(d *caddyfile.Dispenser) error {
	for d.Next() {
		for d.NextBlock(0) {
			switch d.Val() {
			case "db_dsn":
				if !d.NextArg() {
					return d.ArgErr()
				}
				h.DBDSN = d.Val()

			case "db_table":
				if !d.NextArg() {
					return d.ArgErr()
				}
				h.DBTable = d.Val()

			case "db_response_column":
				if !d.NextArg() {
					return d.ArgErr()
				}
				h.DBResponseColumn = d.Val()

			case "db_hostname_column":
				if !d.NextArg() {
					return d.ArgErr()
				}
				h.DBHostnameColumn = d.Val()

			case "db_status_column":
				if !d.NextArg() {
					return d.ArgErr()
				}
				h.DBStatusColumn = d.Val()

			case "db_status_value":
				if !d.NextArg() {
					return d.ArgErr()
				}
				h.DBStatusValue = d.Val()

			case "cache_ttl":
				if !d.NextArg() {
					return d.ArgErr()
				}
				dur, err := time.ParseDuration(d.Val())
				if err != nil {
					return d.Errf("invalid cache_ttl %q: %v", d.Val(), err)
				}
				h.CacheTTL = caddy.Duration(dur)

			case "default_port":
				if !d.NextArg() {
					return d.ArgErr()
				}
				port, err := strconv.Atoi(d.Val())
				if err != nil {
					return d.Errf("invalid default_port %q: %v", d.Val(), err)
				}
				h.DefaultPort = port

			case "allowed_domain_suffix":
				if !d.NextArg() {
					return d.ArgErr()
				}
				h.AllowedDomainSuffix = d.Val()

			case "db_query_timeout":
				if !d.NextArg() {
					return d.ArgErr()
				}
				dur, err := time.ParseDuration(d.Val())
				if err != nil {
					return d.Errf("invalid db_query_timeout %q: %v", d.Val(), err)
				}
				h.DBQueryTimeout = caddy.Duration(dur)

			case "upstream_timeout":
				if !d.NextArg() {
					return d.ArgErr()
				}
				dur, err := time.ParseDuration(d.Val())
				if err != nil {
					return d.Errf("invalid upstream_timeout %q: %v", d.Val(), err)
				}
				h.UpstreamTimeout = caddy.Duration(dur)

			case "health_path":
				if !d.NextArg() {
					return d.ArgErr()
				}
				h.HealthPath = d.Val()

			default:
				return d.Errf("unknown subdirective %q", d.Val())
			}
		}
	}
	return nil
}

// Interface guard.
var _ caddyfile.Unmarshaler = (*PreviewRouter)(nil)
