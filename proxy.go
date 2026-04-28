package previewrouter

import (
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"time"

	"go.uber.org/zap"
)

// setupProxy creates the shared Transport and ReverseProxy that are
// reused across all requests (no per-request allocation).
// ModifyResponse strips all caching headers so proxied responses are
// never cached by browsers, CDNs, or intermediate proxies.
func (h *PreviewRouter) setupProxy() {
	h.transport = &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:          200,
		MaxIdleConnsPerHost:   20,
		IdleConnTimeout:       90 * time.Second,
		ResponseHeaderTimeout: time.Duration(h.UpstreamTimeout),
		ForceAttemptHTTP2:     false,
	}

	h.logger.Info("reverse proxy transport configured",
		zap.Int("max_idle_conns", 200),
		zap.Int("max_idle_conns_per_host", 20),
		zap.Duration("idle_conn_timeout", 90*time.Second),
		zap.Duration("response_header_timeout", time.Duration(h.UpstreamTimeout)),
		zap.Duration("dial_timeout", 10*time.Second),
	)

	h.proxy = &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			target := pr.In.Context().Value(upstreamCtxKey{}).(upstreamTarget)
			upstream := &url.URL{
				Scheme: "http",
				Host:   net.JoinHostPort(target.IP, strconv.Itoa(target.Port)),
			}
			pr.SetURL(upstream)
			pr.Out.Host = pr.In.Host // preserve original Host header
			pr.SetXForwarded()
		},
		Transport:     h.transport,
		FlushInterval: -1, // flush immediately for SSE / HMR streaming
		ModifyResponse: func(resp *http.Response) error {
			resp.Header.Set("Cache-Control", "no-store, no-cache, must-revalidate, proxy-revalidate")
			resp.Header.Set("Pragma", "no-cache")
			resp.Header.Set("Expires", "0")
			resp.Header.Del("ETag")
			resp.Header.Del("Last-Modified")
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			h.logger.Error("upstream connection failed",
				zap.String("host", r.Host),
				zap.String("method", r.Method),
				zap.String("path", r.URL.Path),
				zap.Error(err),
			)
			h.metrics.proxyErrors.Add(1)
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte("Bad Gateway"))
		},
	}
}
