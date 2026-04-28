package previewrouter

import "github.com/caddyserver/caddy/v2"

func init() {
	caddy.RegisterModule(PreviewRouter{})
}

// CaddyModule returns the Caddy module information.
func (PreviewRouter) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "http.handlers.preview_router",
		New: func() caddy.Module { return new(PreviewRouter) },
	}
}
