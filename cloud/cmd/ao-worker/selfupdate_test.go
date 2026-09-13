package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func hashHex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func TestParseAttempt(t *testing.T) {
	cases := map[string]int{"": 0, "0": 0, "1": 1, "3": 3, "-2": 0, "x": 0, "  2 ": 2}
	for in, want := range cases {
		if got := parseAttempt(in); got != want {
			t.Errorf("parseAttempt(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestFileHashDiffers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bin")
	content := []byte("some binary bytes")
	if err := os.WriteFile(path, content, 0o755); err != nil {
		t.Fatal(err)
	}
	if differs, err := fileHashDiffers(path, hashHex(content)); err != nil || differs {
		t.Fatalf("matching hash: differs=%v err=%v, want false/nil", differs, err)
	}
	if differs, err := fileHashDiffers(path, hashHex([]byte("other"))); err != nil || !differs {
		t.Fatalf("mismatched hash: differs=%v err=%v, want true/nil", differs, err)
	}
	if _, err := fileHashDiffers(filepath.Join(dir, "missing"), "abc"); err == nil {
		t.Fatal("missing file: expected an error")
	}
}

// downloadVerifyReplace must write the bytes only when the hash matches, and the
// result must be executable.
func TestDownloadVerifyReplace(t *testing.T) {
	content := []byte("#!/bin/true\nfake worker\n")
	sum := hashHex(content)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(content)
	}))
	defer server.Close()

	dir := t.TempDir()
	dest := filepath.Join(dir, "ao-worker")
	if err := downloadVerifyReplace(context.Background(), server.Client(), server.URL, sum, dest); err != nil {
		t.Fatalf("downloadVerifyReplace: %v", err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(content) {
		t.Fatalf("written bytes = %q, want %q", got, content)
	}
	info, err := os.Stat(dest)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o100 == 0 {
		t.Fatalf("healed binary is not executable: %v", info.Mode())
	}

	// A hash mismatch must be rejected and must not leave a partial file behind
	// under the wrong name.
	dest2 := filepath.Join(dir, "ao-worker-2")
	if err := downloadVerifyReplace(context.Background(), server.Client(), server.URL, hashHex([]byte("wrong")), dest2); err == nil {
		t.Fatal("expected a hash mismatch error")
	}
	if _, err := os.Stat(dest2); !os.IsNotExist(err) {
		t.Fatalf("mismatched download must not create %s", dest2)
	}
}

// healHelper heals into binDir/ao only when neither the healed copy nor the
// baked copy already matches, and it writes the shadowing override.
func TestHealHelper(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	content := []byte("fake ao helper")
	sum := hashHex(content)
	var served int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		served++
		_, _ = w.Write(content)
	}))
	defer server.Close()
	base := server.URL + "/"

	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A baked helper that is already correct means no download and no override.
	baked := filepath.Join(dir, "ao-baked")
	if err := os.WriteFile(baked, content, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AO_WORKER_HELPER_PATH", baked)
	if err := healHelper(context.Background(), server.Client(), logger, base, sum, binDir); err != nil {
		t.Fatalf("healHelper (baked fresh): %v", err)
	}
	if served != 0 {
		t.Fatalf("baked-fresh helper downloaded %d times, want 0", served)
	}
	if _, err := os.Stat(filepath.Join(binDir, "ao")); !os.IsNotExist(err) {
		t.Fatal("baked-fresh helper must not write an override")
	}

	// A stale baked helper triggers a download into binDir/ao.
	if err := os.WriteFile(baked, []byte("stale"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := healHelper(context.Background(), server.Client(), logger, base, sum, binDir); err != nil {
		t.Fatalf("healHelper (baked stale): %v", err)
	}
	override := filepath.Join(binDir, "ao")
	got, err := os.ReadFile(override)
	if err != nil {
		t.Fatalf("override not written: %v", err)
	}
	if string(got) != string(content) {
		t.Fatalf("override bytes = %q, want %q", got, content)
	}

	// A second call is a no-op: the healed override already matches.
	served = 0
	if err := healHelper(context.Background(), server.Client(), logger, base, sum, binDir); err != nil {
		t.Fatalf("healHelper (already healed): %v", err)
	}
	if served != 0 {
		t.Fatalf("already-healed helper downloaded %d times, want 0", served)
	}
}
