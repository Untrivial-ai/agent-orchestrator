//go:build !windows

package agent

import "testing"

func ensurePrivateTestDirectory(t *testing.T, path string) {
	t.Helper()
	if err := ensurePrivateDirectory(path); err != nil {
		t.Fatal(err)
	}
}
