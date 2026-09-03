//go:build !windows

package workerlauncher

func platformFinalPath(path string) (string, error) { return path, nil }
