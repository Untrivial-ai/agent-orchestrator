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

// checkRemoteProjectPath refuses a host-relative path aimed at a remote daemon.
//
// Refuse rather than warn: "~/repo" and "./repo" are resolved by the daemon
// against its own home and its own working directory, so against a remote
// target they silently name a directory on someone else's machine. There is no
// reading under which the operator meant the remote daemon's home — and unlike
// an absolute path, which may legitimately exist on either host, a host-relative
// path cannot be checked by the operator after the fact. A warning on stderr
// would be missed exactly when it matters, in a script or a busy terminal.
//
// Absolute paths are deliberately still allowed: they are meaningful on the
// remote host, and refusing them would make a remote target useless.
func (c *commandContext) checkRemoteProjectPath(path string) error {
	if c.remote == nil {
		return nil
	}
	trimmed := strings.TrimSpace(path)
	switch {
	case strings.HasPrefix(trimmed, "~"):
		return usageError{fmt.Errorf(
			"--path %q is relative to a home directory, and %s targets the remote daemon at %s, "+
				"where it would mean that host's home — pass an absolute path as it exists on that host",
			path, c.remote.source, c.remote.baseURL)}
	case !isAbsForSomeHost(trimmed):
		return usageError{fmt.Errorf(
			"--path %q is relative, and %s targets the remote daemon at %s, where it would be resolved "+
				"against that daemon's working directory — pass an absolute path as it exists on that host",
			path, c.remote.source, c.remote.baseURL)}
	}
	return nil
}

// isAbsForSomeHost reports whether p is an absolute path on ANY host AO might
// talk to, rather than on the machine running the CLI.
//
// filepath.IsAbs is the wrong question here and shipping it was a bug: it
// judges by the local OS, so on Windows it calls "/srv/repo" relative — and a
// Windows operator targeting a Linux remote host, which is exactly what remote
// execution creates, was refused a perfectly valid path on that host. The
// destination filesystem is the remote daemon's, so the CLI must accept every
// absolute form and let the daemon judge its own.
//
// Deliberately NOT accepted, because both are host-relative even on Windows:
// a bare drive-relative path ("C:foo") and a single leading backslash
// ("\foo", relative to the current drive).
func isAbsForSomeHost(p string) bool {
	if strings.HasPrefix(p, "/") {
		return true // POSIX absolute
	}
	if strings.HasPrefix(p, `\\`) {
		return true // Windows UNC: \\server\share
	}
	// Windows drive-absolute: C:\foo or C:/foo (a slash after the colon is
	// what separates this from drive-RELATIVE "C:foo").
	if len(p) >= 3 && p[1] == ':' && (p[2] == '\\' || p[2] == '/') {
		return (p[0] >= 'A' && p[0] <= 'Z') || (p[0] >= 'a' && p[0] <= 'z')
	}
	return false
}

// checkRemoteImplicitProject refuses to resolve a project from a local signal
// when the command targets a remote daemon.
//
// Refuse rather than guess, for the same reason as checkRemoteProjectPath:
// AO_PROJECT_ID, AO_SESSION_ID and the current directory all describe THIS
// machine, but with a remote target they are matched against the remote
// daemon's projects. Measured: `ao spawn --url <remote>` run inside any AO
// session inherits an AO_PROJECT_ID the operator never typed and spawns
// against whatever project on the remote host happens to share that id — and
// cwd matching picks a remote project whenever the two machines' paths
// coincide. A session started on the wrong machine cannot be caught after the
// fact from the output, which names only the session it created.
//
// So --project is required for a remote target: it is the one input that means
// the same thing on both hosts.
func (c *commandContext) checkRemoteImplicitProject(explicit string) error {
	if c.remote == nil || strings.TrimSpace(explicit) != "" {
		return nil
	}
	signal := "the current directory"
	switch {
	case strings.TrimSpace(os.Getenv("AO_PROJECT_ID")) != "":
		signal = "AO_PROJECT_ID"
	case strings.TrimSpace(os.Getenv("AO_SESSION_ID")) != "":
		signal = "AO_SESSION_ID"
	}
	return usageError{fmt.Errorf(
		"%s describes this machine, and %s targets the remote daemon at %s, where it would select "+
			"a project on that host — pass --project <id> as it exists on that host "+
			"(list them with `ao project ls --url %s`)",
		signal, c.remote.source, c.remote.baseURL, c.remote.baseURL)}
}

// authorize presents the remote connection password. Loopback calls carry no
// credential: the local daemon's loopback listener has no auth at all.
func (c *commandContext) authorize(req *http.Request) {
	if c.remote != nil {
		req.Header.Set("Authorization", "Bearer "+c.remote.token)
	}
}
