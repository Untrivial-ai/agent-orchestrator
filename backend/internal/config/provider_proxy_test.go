package config

import (
	"path/filepath"
	"testing"
)

func TestAccountHelperBinaryOverride(t *testing.T) {
	root := t.TempDir()
	for _, value := range []string{"", filepath.Join(root, "build with spaces", "ao-proxy-host")} {
		t.Setenv("AO_DATA_DIR", root)
		t.Setenv("AO_RUN_FILE", filepath.Join(root, "running.json"))
		t.Setenv("AO_PROXY_HOST_BINARY", value)
		cfg, err := Load()
		if err != nil || cfg.ProxyHostBinary != value || cfg.DataDir != root {
			t.Fatalf("override %q: helper=%q data=%q err=%v", value, cfg.ProxyHostBinary, cfg.DataDir, err)
		}
	}
}
