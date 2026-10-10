//go:build windows

package proxyhost

import "golang.org/x/sys/windows"

var detached = &windows.SysProcAttr{CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP, HideWindow: true}
