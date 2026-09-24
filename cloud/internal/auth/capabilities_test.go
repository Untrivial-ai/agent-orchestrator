package auth

import (
	"slices"
	"testing"
)

func TestParseOrganizationCapabilities(t *testing.T) {
	cases := []struct {
		name     string
		metadata map[string]string
		want     []string
	}{
		{"nil metadata", nil, nil},
		{"no capabilities key", map[string]string{"other": "x"}, nil},
		{"blank", map[string]string{"capabilities": "   "}, nil},
		{"single", map[string]string{"capabilities": "coder"}, []string{"coder"}},
		{"comma separated + trim", map[string]string{"capabilities": "coder, feature-x"}, []string{"coder", "feature-x"}},
		{"lowercased + deduped", map[string]string{"capabilities": "Coder,CODER,coder"}, []string{"coder"}},
		{"drops empty entries", map[string]string{"capabilities": "coder,,x,"}, []string{"coder", "x"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseOrganizationCapabilities(tc.metadata)
			if !slices.Equal(got, tc.want) {
				t.Fatalf("parseOrganizationCapabilities(%v) = %v, want %v", tc.metadata, got, tc.want)
			}
		})
	}
}
