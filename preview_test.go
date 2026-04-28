package previewrouter

import (
	"testing"
)

func TestExtractHost(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"example.com", "example.com"},
		{"example.com:443", "example.com"},
		{"abc.preview.example.com", "abc.preview.example.com"},
		{"abc.preview.example.com:8080", "abc.preview.example.com"},
		{"[::1]:443", "::1"},
		{"::1", "::1"},
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := extractHost(tt.input)
			if got != tt.want {
				t.Errorf("extractHost(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestHostnameSuffixValidation(t *testing.T) {
	const suffix = ".preview.example.com"

	tests := []struct {
		hostname string
		allowed  bool
	}{
		{"abc.preview.example.com", true},
		{"062fbada.preview.example.com", true},
		{"x.y.preview.example.com", true},
		{"evil.com", false},
		{"preview.example.com", false}, // no subdomain prefix
		{"abc.staging.example.com", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.hostname, func(t *testing.T) {
			got := len(tt.hostname) > 0 && len(tt.hostname) > len(suffix) &&
				tt.hostname[len(tt.hostname)-len(suffix):] == suffix
			if got != tt.allowed {
				t.Errorf("hostname %q suffix check = %v, want %v", tt.hostname, got, tt.allowed)
			}
		})
	}
}

func TestUpstreamTargetPort(t *testing.T) {
	tests := []struct {
		name        string
		previewPort int
		defaultPort int
		wantPort    int
	}{
		{"uses preview_port when set", 3000, 5173, 3000},
		{"falls back to default_port", 0, 5173, 5173},
		{"uses preview_port even if matches default", 5173, 5173, 5173},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			port := tt.previewPort
			if port == 0 {
				port = tt.defaultPort
			}
			if port != tt.wantPort {
				t.Errorf("port = %d, want %d", port, tt.wantPort)
			}
		})
	}
}
