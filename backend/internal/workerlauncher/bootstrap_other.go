//go:build !windows

package workerlauncher

import "errors"

type ReadyMessage struct{}

func Bootstrap(string, string) (ReadyMessage, error) {
	return ReadyMessage{}, errors.New("worker launcher: Windows only")
}
func RunWorker(string, string, string, string, string) error {
	return errors.New("worker launcher: Windows only")
}
func RunLauncherService(string) error             { return errors.New("worker launcher: Windows only") }
func AllowedEnvironmentKeys() map[string]struct{} { return map[string]struct{}{} }
