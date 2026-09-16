//go:build !windows

package gitworktree

func isRetryableRemoveError(error) bool { return false }
