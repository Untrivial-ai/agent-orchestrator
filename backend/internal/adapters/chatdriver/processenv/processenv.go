// Package processenv builds child-process environments shared by Chat drivers.
package processenv

import (
	"os"
	"sort"
	"strings"
)

// Merge overlays session-specific values on the daemon environment and returns
// the KEY=VALUE form expected by os/exec. Sorting makes launches deterministic
// enough to inspect and compare in tests and process diagnostics.
func Merge(overlay map[string]string) []string {
	merged := make(map[string]string, len(os.Environ())+len(overlay))
	for _, entry := range os.Environ() {
		if key, value, ok := strings.Cut(entry, "="); ok {
			merged[key] = value
		}
	}
	for key, value := range overlay {
		merged[key] = value
	}
	return entries(merged)
}

func entries(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	out := make([]string, 0, len(keys))
	for _, key := range keys {
		out = append(out, key+"="+values[key])
	}
	return out
}

// FingerprintEntries excludes controller credentials that rotate on daemon
// adoption while retaining provider-effective session and project environment.
func FingerprintEntries(values map[string]string) []string {
	stable := make(map[string]string, len(values))
	for key, value := range values {
		switch key {
		case "AO_BROWSER_CAPABILITY", "AO_BROWSER_RUNTIME_TOKEN", "AO_BROWSER_RUNTIME_TOKEN_STDIN":
			continue
		}
		stable[key] = value
	}
	return entries(stable)
}
