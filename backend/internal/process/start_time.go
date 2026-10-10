package process

import "errors"

// ErrNotRunning identifies a missing or zombie process. Ownership callers may
// treat a descendant as gone while still requiring their target's main PIDs.
var ErrNotRunning = errors.New("process is not running")
