//go:build !windows

package workerlauncher

import "errors"

type PreparedLaunch struct{ LauncherPath, ConfigPath, ManifestPath string }

func PrepareLaunch(string, string, []string, []string) (PreparedLaunch, error) {
	return PreparedLaunch{}, errors.New("worker launcher: Windows only")
}
