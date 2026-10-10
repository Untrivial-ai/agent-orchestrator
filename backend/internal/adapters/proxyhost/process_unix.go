//go:build !windows

package proxyhost

import "syscall"

var detached = &syscall.SysProcAttr{Setsid: true}
