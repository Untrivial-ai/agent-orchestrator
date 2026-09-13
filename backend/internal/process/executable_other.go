//go:build !darwin

package process

func resolveExecutable(name string) string { return name }
