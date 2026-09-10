package agentcreds

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"time"
)

// keychainTimeout hard-bounds the keychain read.
//
// A measured read takes 75–110ms, prompts nothing, and works from a detached
// process with no TTY. But that is a property of how Claude Code stores this
// particular item today, not a platform guarantee — a differently-stored item
// can put a GUI unlock dialog in front of the read and block until someone
// clicks it. The design must not depend on the fast path, so the call is
// capped and a timeout resolves to "no credential", never to a hang.
const keychainTimeout = 3 * time.Second

// Keychain exit codes, measured.
const (
	// keychainExitNotFound is `security`'s "item could not be found". The
	// user has no subscription login stored; fall through to the file.
	keychainExitNotFound = 44
	// keychainExitDenied covers a locked keychain or a denied ACL. It returns
	// immediately rather than hanging, and means "cannot tell", not "absent".
	keychainExitDenied = 128
)

// readKeychain reads the Claude Code subscription token from the macOS
// keychain via the `security` helper.
//
// Every failure mode resolves the same way — no credential, caller reports
// Unknown — so the distinctions below are for diagnosis, not control flow.
// That is deliberate: a locked keychain must never be reported as a missing
// or invalid credential, because the user is very likely signed in perfectly
// well and simply has the keychain locked.
func readKeychain(ctx context.Context, opts ResolveOptions) (string, bool) {
	runner := opts.Runner
	if runner == nil {
		runner = execCommand
	}
	probeCtx, cancel := context.WithTimeout(ctx, keychainTimeout)
	defer cancel()

	out, err := runner(probeCtx, "security",
		"find-generic-password", "-s", "Claude Code-credentials", "-w")
	if probeCtx.Err() != nil {
		// A GUI unlock dialog is blocking. Give up rather than wait on a
		// person who may not be at the machine.
		return "", false
	}
	if err != nil {
		switch keychainExitCode(err) {
		case keychainExitNotFound, keychainExitDenied:
			return "", false
		default:
			return "", false
		}
	}
	token, ok := oauthTokenFromCredentialsJSON([]byte(out))
	if ok {
		return token, true
	}
	// Older entries store the bare token rather than a JSON document.
	if raw := strings.TrimSpace(lastNonEmptyLine(string(out))); raw != "" && !strings.HasPrefix(raw, "{") {
		return raw, true
	}
	return "", false
}

func keychainExitCode(err error) int {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}
