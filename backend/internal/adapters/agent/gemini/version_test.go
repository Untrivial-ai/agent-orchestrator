package gemini

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestVersionProbeAcceptsSupportedCLIAndRejectsOldCLI(t *testing.T) {
	for _, tc := range []struct {
		version   string
		wantError bool
	}{{"0.60.0", false}, {"0.59.9", true}, {"unrecognized", true}} {
		t.Run(tc.version, func(t *testing.T) {
			name, body := "gemini", "#!/bin/sh\necho "+tc.version+"\n"
			if runtime.GOOS == "windows" {
				name, body = "gemini.cmd", "@echo off\r\necho "+tc.version+"\r\n"
			}
			binary := filepath.Join(t.TempDir(), name)
			if err := os.WriteFile(binary, []byte(body), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := checkVersion(context.Background(), binary); (err != nil) != tc.wantError {
				t.Fatalf("checkVersion = %v", err)
			}
		})
	}
}
