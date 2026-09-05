//go:build !darwin && !windows && !linux

package conpty

import (
	"context"
	"fmt"
)

func legacyListenerPID(context.Context, string, int) (int, error) {
	return 0, fmt.Errorf("protocol-v2 pty-host recovery is unsupported on this platform")
}

func legacyProcessIdentityForPID(context.Context, int) (legacyProcessIdentity, error) {
	return legacyProcessIdentity{}, fmt.Errorf("protocol-v2 pty-host recovery is unsupported on this platform")
}

func legacyProcessIncarnationForPID(context.Context, int) (legacyProcessIncarnation, error) {
	return legacyProcessIncarnation{}, fmt.Errorf("protocol-v2 pty-host recovery is unsupported on this platform")
}
