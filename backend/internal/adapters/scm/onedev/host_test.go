package onedev

import "testing"

func TestParseAllowedHost(t *testing.T) {
	tests := []struct {
		name      string
		raw       string
		wantOK    bool
		scheme    string
		authority string
		apiBase   string
	}{
		{
			name: "bare hostname defaults to https", raw: "onedev.example.com", wantOK: true,
			scheme: "https", authority: "onedev.example.com",
			apiBase: "https://onedev.example.com/~api",
		},
		{
			name: "hostname with port defaults to https", raw: "onedev.example.com:6610", wantOK: true,
			scheme: "https", authority: "onedev.example.com:6610",
			apiBase: "https://onedev.example.com:6610/~api",
		},
		{
			name: "explicit http is preserved", raw: "http://10.0.0.30:6610", wantOK: true,
			scheme: "http", authority: "10.0.0.30:6610",
			apiBase: "http://10.0.0.30:6610/~api",
		},
		{
			name: "explicit https", raw: "https://git.example.com", wantOK: true,
			scheme: "https", authority: "git.example.com",
			apiBase: "https://git.example.com/~api",
		},
		{
			name: "case and whitespace are normalized", raw: "  HTTP://OneDev.Example.COM:6610  ", wantOK: true,
			scheme: "http", authority: "onedev.example.com:6610",
			apiBase: "http://onedev.example.com:6610/~api",
		},
		{
			name: "ipv6 literal", raw: "http://[::1]:6610", wantOK: true,
			scheme: "http", authority: "[::1]:6610",
			apiBase: "http://[::1]:6610/~api",
		},
		{
			name: "trailing slash is tolerated", raw: "http://10.0.0.30:6610/", wantOK: true,
			scheme: "http", authority: "10.0.0.30:6610",
			apiBase: "http://10.0.0.30:6610/~api",
		},
		{name: "empty", raw: "", wantOK: false},
		{name: "whitespace only", raw: "   ", wantOK: false},
		{name: "unsupported scheme", raw: "ssh://onedev.example.com:6611", wantOK: false},
		{name: "path is rejected", raw: "http://10.0.0.30:6610/~api", wantOK: false},
		{name: "query is rejected", raw: "http://10.0.0.30:6610?a=b", wantOK: false},
		{name: "credentials are rejected", raw: "http://user:pw@10.0.0.30:6610", wantOK: false},
		{name: "missing host", raw: "http://", wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseAllowedHost(tt.raw)
			if !tt.wantOK {
				if err == nil {
					t.Fatalf("parseAllowedHost(%q) = %+v, want error", tt.raw, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseAllowedHost(%q): %v", tt.raw, err)
			}
			if got.scheme != tt.scheme {
				t.Errorf("scheme = %q, want %q", got.scheme, tt.scheme)
			}
			if got.authority != tt.authority {
				t.Errorf("authority = %q, want %q", got.authority, tt.authority)
			}
			if got.apiBase() != tt.apiBase {
				t.Errorf("apiBase() = %q, want %q", got.apiBase(), tt.apiBase)
			}
		})
	}
}

func TestHostnameOf(t *testing.T) {
	tests := []struct {
		authority string
		want      string
	}{
		{"onedev.example.com:6610", "onedev.example.com"},
		{"onedev.example.com", "onedev.example.com"},
		{"10.0.0.30:6611", "10.0.0.30"},
		{"[::1]:6610", "::1"},
		{"[::1]", "::1"},
		{"OneDev.Example.COM:6610", "onedev.example.com"},
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.authority, func(t *testing.T) {
			if got := hostnameOf(tt.authority); got != tt.want {
				t.Fatalf("hostnameOf(%q) = %q, want %q", tt.authority, got, tt.want)
			}
		})
	}
}

func TestNormalizeHostKey(t *testing.T) {
	tests := []struct {
		raw  string
		want string
	}{
		{"http://10.0.0.30:6610", "10.0.0.30:6610"},
		{"10.0.0.30:6610", "10.0.0.30:6610"},
		{"OneDev.Example.COM", "onedev.example.com"},
		// A value that is not a parseable host entry falls back to plain
		// normalization rather than erroring, so a malformed AO_ONEDEV_HOST_TOKENS
		// key simply never matches.
		{"not a host!!", "not a host!!"},
	}
	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			if got := normalizeHostKey(tt.raw); got != tt.want {
				t.Fatalf("normalizeHostKey(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}
