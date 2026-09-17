//go:build windows

package agent

import "golang.org/x/sys/windows"

func replaceCodexFile(source, target string) error {
	sourcePtr, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	targetPtr, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(sourcePtr, targetPtr, codexWindowsAtomicReplaceFlags())
}
