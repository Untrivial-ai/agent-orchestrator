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

// Keychain service names Claude Code has used to store its credential.
const (
	// keychainServiceCredentials is the older service. It stores a JSON
	// document with an OAuth access token, or occasionally a bare token.
	keychainServiceCredentials = "Claude Code-credentials"
	// keychainServiceManagedKey is the service Claude Code v2.1.268+ uses
	// for the /login managed key. The value is a raw sk-ant-api* key.
	keychainServiceManagedKey = "Claude Code"
)

// readKeychain reads the Claude Code subscription token from the macOS
// keychain via the `security` helper.
//
// Every failure mode resolves the same way — no credential, caller reports
// Unknown — so the distinctions below are for diagnosis, not control flow.
// That is deliberate: a locked keychain must never be reported as a missing
// or invalid credential, because the user is very likely signed in perfectly
// well and simply has the keychain locked.
func readKeychain(ctx context.Context, opts ResolveOptions) (string, Kind, bool) {
	runner := opts.Runner
	if runner == nil {
		runner = execCommand
	}
	probeCtx, cancel := context.WithTimeout(ctx, keychainTimeout)
	defer cancel()

	// Try the older "Claude Code-credentials" service first. It stores a JSON
	// document with an OAuth access token, or occasionally a bare token.
	out, err := runner(probeCtx, "security",
		"find-generic-password", "-s", keychainServiceCredentials, "-w")
	if probeCtx.Err() != nil {
		return "", "", false
	}
	if err == nil {
		if token, ok := oauthTokenFromCredentialsJSON([]byte(out)); ok {
			return token, KindOAuthToken, true
		}
		// Older entries store the bare token rather than a JSON document.
		if raw := strings.TrimSpace(lastNonEmptyLine(string(out))); raw != "" && !strings.HasPrefix(raw, "{") {
			return raw, KindOAuthToken, true
		}
	}

	// Fall through to "Claude Code", the service Claude Code v2.1.268+ uses
	// for the /login managed key. The value is a raw sk-ant-api* key, though
	// a setup token (sk-ant-oat*) could also land here, so the Kind is
	// chosen by prefix rather than assumed.
	apiKeyOut, apiKeyErr := runner(probeCtx, "security",
		"find-generic-password", "-s", keychainServiceManagedKey, "-w")
	if probeCtx.Err() != nil {
		return "", "", false
	}
	if apiKeyErr != nil {
		return "", "", false
	}
	if raw := strings.TrimSpace(lastNonEmptyLine(string(apiKeyOut))); raw != "" && !strings.HasPrefix(raw, "{") {
		return raw, kindForToken(raw), true
	}
	if token, ok := oauthTokenFromCredentialsJSON([]byte(apiKeyOut)); ok {
		return token, KindOAuthToken, true
	}
	return "", "", false
}

// kindForToken selects the auth header from the token prefix. Claude Code
// console keys are sk-ant-api* (x-api-key); setup tokens and subscription
// logins are sk-ant-oat* (Bearer). Anything unrecognised defaults to API key
// so the probe still runs rather than silently skipping a credential.
func kindForToken(token string) Kind {
	if strings.HasPrefix(strings.TrimSpace(token), "sk-ant-oat") {
		return KindOAuthToken
	}
	return KindAPIKey
}

func keychainExitCode(err error) int {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}
