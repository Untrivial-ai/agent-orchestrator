package authutil

import (
	"bytes"
	"context"
	"errors"
	"strings"
)

// GenericPassword reads a macOS generic-password item using fixed, caller-owned
// service/account constants. Never pass selectors derived from project config.
// The returned secret is only for local parsing, never Evidence.Source or logs.
func GenericPassword(ctx context.Context, d Dependencies, service, account string) ([]byte, error) {
	if d.goos() != "darwin" {
		return nil, errors.New("credential keychain unavailable")
	}
	if strings.TrimSpace(service) == "" || strings.TrimSpace(account) == "" {
		return nil, errors.New("credential keychain requires service and account")
	}
	out, err := RunCommand(ctx, d, "/usr/bin/security", "find-generic-password", "-s", service, "-a", account, "-w")
	if err != nil {
		return nil, err
	}
	return bytes.TrimSpace(out), nil
}
