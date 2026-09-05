package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/config"
)

// A remote target replaces the local run-file handshake: instead of reading
// running.json and gating on a live local PID, every daemon call goes to the
// given base URL carrying the daemon's connection password as a Bearer token —
// the same credential channel the mobile client uses (ADR 0001).
//
// It is opt-in and never inferred: with no --url and no AO_URL, the CLI keeps
// its loopback-only behavior exactly as before.
type remoteTarget struct {
	baseURL string
	token   string
	// source names where the URL came from ("--url" or "AO_URL") so an error can
	// tell the user which one is pointing them off-box.
	source string
}

// remotesFileName holds saved remote daemons under the AO home directory. It
// carries connection passwords in plaintext, so it must be mode 0600.
const remotesFileName = "remotes.json"

// remotesFile mirrors the mobile app's multi-node store
// (packages/mobile/lib/config.ts): a list of saved nodes, each labelled, each
// with its own connection password. The CLI has no OS keystore to split the
// secret into, so the password lives in the file and the file must be 0600.
type remotesFile struct {
	Remotes []remoteEntry `json:"remotes"`
}

type remoteEntry struct {
	Label    string `json:"label,omitempty"`
	URL      string `json:"url"`
	Password string `json:"password"`
}

// resolveRemoteTarget resolves the remote daemon this invocation targets, or
// nil for the default local daemon. The URL comes from --url or AO_URL; the
// credential from AO_TOKEN or a matching entry in ~/.ao/remotes.json.
func resolveRemoteTarget(flagURL string) (*remoteTarget, error) {
	raw, source := strings.TrimSpace(flagURL), "--url"
	if raw == "" {
		raw, source = strings.TrimSpace(os.Getenv("AO_URL")), "AO_URL"
	}
	if raw == "" {
		return nil, nil
	}
	base, err := normalizeRemoteURL(raw)
	if err != nil {
		return nil, err
	}
	token := strings.TrimSpace(os.Getenv("AO_TOKEN"))
	if token == "" {
		entry, err := lookupRemoteEntry(base)
		if err != nil {
			return nil, err
		}
		path, _ := remotesFilePath()
		switch {
		case entry == nil:
			return nil, fmt.Errorf("no connection password for %s — set AO_TOKEN or add an entry for it to %s", base, path)
		case strings.TrimSpace(entry.Password) == "":
			return nil, fmt.Errorf("the entry for %s in %s has an empty password — set its password, or use AO_TOKEN", base, path)
		}
		token = strings.TrimSpace(entry.Password)
	}
	return &remoteTarget{baseURL: base, token: token, source: source}, nil
}

// normalizeRemoteURL turns a user-supplied target into a base URL with no
// trailing slash. A bare "host:3011" is accepted and assumed plaintext http,
// matching the LAN listener (ADR 0001 is deliberately http-only).
func normalizeRemoteURL(raw string) (string, error) {
	// A URL-embedded credential is never trusted — the connection password comes
	// from AO_TOKEN or remotes.json. Silently dropping it would leave the user
	// certain they had passed a password and then told there was none, so say so
	// instead. Checked first, and reported without quoting the URL, so no later
	// error path can echo the credential into a message or a log line.
	if hasUserinfo(raw) {
		return "", errors.New("invalid daemon URL: it must not carry a username or password — " +
			"pass the connection password in AO_TOKEN or a ~/.ao/remotes.json entry")
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid daemon URL %q: %w", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("invalid daemon URL %q: scheme must be http or https", raw)
	}
	if u.Host == "" {
		return "", fmt.Errorf("invalid daemon URL %q: missing host", raw)
	}
	return strings.TrimRight(u.Scheme+"://"+u.Host+u.Path, "/"), nil
}

// hasUserinfo reports whether raw's authority component carries userinfo. It
// works textually, before url.Parse, so that a malformed URL cannot slip a
// credential into the parse error — and it catches the scheme-less form
// ("user:pw@host:3011") too. An "@" in the path or query is not authority.
func hasUserinfo(raw string) bool {
	authority := raw
	if i := strings.Index(authority, "://"); i >= 0 {
		authority = authority[i+3:]
	}
	if i := strings.IndexAny(authority, "/?#"); i >= 0 {
		authority = authority[:i]
	}
	return strings.Contains(authority, "@")
}

func remotesFilePath() (string, error) {
	dir, err := config.StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, remotesFileName), nil
}

// lookupRemoteEntry returns the saved entry for base, or nil when the file does
// not exist or holds no entry for it. A nil entry and an entry with an empty
// password are different problems, so the caller can say which one it is. A file
// readable by anyone but the owner is refused rather than used: it is a
// plaintext credential store.
func lookupRemoteEntry(base string) (*remoteEntry, error) {
	path, err := remotesFilePath()
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	// Windows file modes do not carry meaningful group/other bits, so the check
	// would reject every file there.
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("%s holds connection passwords and is readable by others (mode %04o) — run: chmod 600 %s",
			path, info.Mode().Perm(), path)
	}
	raw, err := os.ReadFile(path) // #nosec G304 -- fixed path under the AO home directory.
	if err != nil {
		return nil, err
	}
	var file remotesFile
	if err := json.Unmarshal(raw, &file); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	for _, entry := range file.Remotes {
		saved, err := normalizeRemoteURL(entry.URL)
		if err != nil {
			continue // a hand-edited entry must not break every other one
		}
		if saved == base {
			return &entry, nil
		}
	}
	return nil, nil
}

// authorize presents the remote connection password. Loopback calls carry no
// credential: the local daemon's loopback listener has no auth at all.
func (c *commandContext) authorize(req *http.Request) {
	if c.remote != nil {
		req.Header.Set("Authorization", "Bearer "+c.remote.token)
	}
}
