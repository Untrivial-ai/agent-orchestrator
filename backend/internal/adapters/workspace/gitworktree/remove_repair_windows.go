//go:build windows

package gitworktree

// Windows read-only is a file attribute, not a directory permission, and
// ERROR_ACCESS_DENIED is already handled by the sharing-violation retry loop.
// No Unix-style permission repair applies.
func repairRemovePermissions(string) bool { return false }

func isPermissionRemoveError(error) bool { return false }
