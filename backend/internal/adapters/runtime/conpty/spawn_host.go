//go:build windows || darwin || linux

package conpty

import (
	"regexp"
	"time"
)

// readyRE matches the "READY:<pid> <port>" line printed by RunHost.
var readyRE = regexp.MustCompile(`READY:(\d+) (\d+)`)

const spawnReadyTimeout = 10 * time.Second

// maxCapturedStderr bounds how much pty-host stderr we retain for diagnostics.
const maxCapturedStderr = 8192
