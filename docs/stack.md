# AO technical stack

This is the source of truth for library and runtime choices in the AO rewrite.
Keep this document about durable technology decisions; use `STATUS.md` for
implementation progress and `architecture.md` for component behavior and
invariants.

## Principles

- Prefer the Go standard library until a small dependency clearly earns its
  place.
- Keep the backend daemon boring: explicit process control, explicit SQL,
  narrow adapters, and observable failure modes.
- Shell out where AO needs the user's real developer-machine behavior, especially
  for Git and terminal multiplexers.
- Keep high-volume terminal output out of SQLite; store structured state in the
  database and stream/log payload-heavy data separately.

## Accepted stack

| Area               | Decision                                                                                        | Status                 | Rationale                                                                                                                           |
| ------------------ | ----------------------------------------------------------------------------------------------- | ---------------------- | ----------------------------------------------------------------------------------------------------------------------------------- |
| Backend language   | Go 1.25.7                                                                                       | Implemented            | Matches `backend/go.mod`; small daemon, strong stdlib, easy local distribution.                                                     |
| Backend core       | Go stdlib                                                                                       | Implemented            | Domain, lifecycle, session, and adapter contracts should stay dependency-light.                                                     |
| Frontend shell     | Electron + TypeScript                                                                           | Implemented            | Local desktop control plane paired with the daemon.                                                                                 |
| Runtime adapter    | Native detached PTY host (new macOS sessions), `tmux` (Linux and legacy macOS handles), ConPTY host (Windows) | Implemented            | Versioned opaque handles keep existing macOS sessions on tmux while new sessions stream the PTY directly.                           |
| Terminal PTY       | `github.com/creack/pty`                                                                         | Implemented            | PTY-backed terminal sessions with resize/input/output control.                                                                      |
| Terminal viewport  | `github.com/unixshells/vt-go`                                                                   | Implemented            | Gives detached PTY hosts a rendered current-screen model for safety checks; the ring remains historical attach replay only.          |
| Git/worktrees      | `git` CLI via `os/exec`                                                                         | Implemented            | Uses real repo behavior, credentials, hooks, LFS, submodules, and user config.                                                      |
| HTTP API           | `net/http` + `github.com/go-chi/chi/v5`                                                         | Implemented            | Lightweight, idiomatic router without committing AO to a large web framework.                                                       |
| WebSocket          | `github.com/coder/websocket`                                                                    | Implemented            | Small WebSocket library for terminal streaming.                                                                                     |
| Storage            | SQLite in WAL mode via `database/sql`                                                           | Implemented            | Local daemon, single writer, many dashboard/API reads, no external DB setup.                                                        |
| SQLite driver      | `modernc.org/sqlite`                                                                            | Implemented            | Current pure-Go driver in `backend/internal/storage/sqlite`; keep it swappable behind `database/sql`.                               |
| SQL generation     | `github.com/sqlc-dev/sqlc`                                                                      | Implemented            | Hand-written SQL with generated typed methods from `backend/sqlc.yaml`.                                                             |
| Migrations         | `github.com/pressly/goose/v3`                                                                   | Implemented            | Simple SQL migrations for the embedded/local database.                                                                              |
| CLI                | `github.com/spf13/cobra`                                                                        | Implemented            | Standard command structure for daemon startup, diagnostics, and admin commands.                                                     |
| Config             | stdlib environment loading + SQLite-backed state/config                                         | Implemented / evolving | `internal/config` handles daemon env/defaults; durable product config belongs in SQLite, so no config framework is selected for V1. |
| Logging            | `log/slog`                                                                                      | Implemented            | Stdlib structured logging before adding another logging dependency.                                                                 |
| OpenAPI generation | `github.com/swaggest/openapi-go`, `github.com/swaggest/jsonschema-go`, `gopkg.in/yaml.v3`       | Implemented            | Generated OpenAPI keeps route contracts close to Go DTOs.                                                                           |
| Testing            | stdlib `testing`                                                                                | Implemented            | Keep pure domain logic and adapter contracts easy to test.                                                                          |
| Test assertions    | `github.com/stretchr/testify/require`                                                           | Planned if needed      | Concise assertions for higher-level adapter and integration tests; do not add unless tests benefit.                                 |
| Packaging          | `github.com/goreleaser/goreleaser`                                                              | Planned                | Cross-platform release automation, checksums, and future Homebrew support.                                                          |

## Pending decisions

### SQLite driver validation

Current main uses `modernc.org/sqlite`. Before release packaging is locked,
validate `github.com/ncruces/go-sqlite3/driver` against AO's WAL, migration,
and `change_log`/CDC workload. It is the preferred no-CGO candidate if it passes
compatibility and performance checks.

Keep the driver behind `database/sql` so the persistence layer can switch
drivers without changing store interfaces.

Required SQLite setup:

```sql
PRAGMA journal_mode = WAL;
PRAGMA busy_timeout = 5000;
PRAGMA foreign_keys = ON;
PRAGMA synchronous = NORMAL;
```

### Config model

Current daemon config is stdlib env/default loading. Project and product config
should be persisted in SQLite when it needs durability or user editing. Do not
add `github.com/spf13/viper` or `github.com/knadh/koanf` unless a real file-based
config surface appears.

## Explicitly avoided for V1

| Avoid                                                          | Reason                                                                                        |
| -------------------------------------------------------------- | --------------------------------------------------------------------------------------------- |
| GORM                                                           | AO needs explicit transactional SQL and CDC-triggered writes.                                 |
| Gin/Fiber                                                      | `net/http` + `chi` is enough for a local daemon API.                                          |
| `go-git` as the primary Git engine                             | AO should match installed Git behavior, credentials, hooks, LFS, submodules, and user config. |
| `github.com/spf13/viper` / `github.com/knadh/koanf` by default | Env/default loading plus SQLite-backed config is enough for V1.                               |
| Temporal / NATS / Kafka / Redis                                | V1 is a local daemon with SQLite and CDC, not a distributed control plane.                    |
| Full plugin framework                                          | Keep adapter interfaces narrow until product needs justify a plugin runtime.                  |
| Multi-sink CDC fan-out                                         | Start with one durable local delivery path; add fan-out later if needed.                      |

## Current stack mapping

```txt
Go daemon
  net/http + github.com/go-chi/chi/v5
  github.com/coder/websocket
  github.com/creack/pty
  github.com/unixshells/vt-go (ConPTY rendered viewport)
  tmux runtime adapter via os/exec (conpty on Windows), selected by runtimeselect
  git worktree adapter via git CLI
  SQLite via database/sql + modernc.org/sqlite
  github.com/sqlc-dev/sqlc generated queries
  github.com/pressly/goose/v3 migrations
  log/slog
  github.com/spf13/cobra CLI
  SQLite change_log + CDC poller
```

This stack supports the current architecture: durable session/PR/project facts,
derived display status, SQLite `change_log` CDC, terminal sessions, and real Git
worktrees.
