//go:build !windows

package codexappserver

import "testing"

func protectManagedHomeForTest(t *testing.T, _ string) {
	t.Helper()
}
