# Dashboard-Surfaced Legacy Import Offer — Implementation Plan

> **Historical status:** This records the PR #320 import-offer design at its
> original repository and branch. Do not use its branch, repository, or API
> shape as current guidance. Current import code also handles workspace
> validation and Git preparation. See [the current import service](../../backend/internal/service/importer/importer.go)
> and [current documentation](../README.md).

> **For agentic workers:** implement task-by-task; each task ends with a green test + a commit. Steps use `- [ ]`.

**Goal:** Replace the first-boot CLI import prompt with a daemon API (`GET`/`POST /api/v1/import`) and a dashboard banner. **Scope: projects + per-project settings only** (no orchestrator sessions, no transcript relocation), a faithful port of `aoagents/ReverbCode` PR #320.

**Architecture:** `ao start` becomes headless. The `legacyimport` engine is simplified to import projects only. A new `service/importer` wraps it with a detection probe (`Status`) and a trigger (`Run`) that writes through the daemon's shared store. An HTTP controller exposes both; the Electron renderer polls status and shows an `ImportOffer` banner. Because startup is now headless, the Electron main process also gains always-on discovery of a daemon it didn't spawn.

**Tech Stack:** Go (chi, sqlc, code-first OpenAPI via `cmd/genspec`), React + @tanstack/react-query + openapi-fetch, Electron, vitest.

## Global Constraints

- Module path: `github.com/aoagents/agent-orchestrator/backend`.
- **No em dashes** anywhere (prose, comments, copy). Use `.`/`,`/`(...)`.
- `openapi.yaml` and `frontend/src/api/schema.ts` are **generated** (never hand-edit); change the Go reflection source then run `npm run api:spec && npm run api:ts`.
- All app state under `~/.ao` only (already enforced; don't regress).
- Branch: `ao/agent-orchestrator-3/import-offer` (sibling of `…/root`). PR target `main` on `AgentWrapper/agent-orchestrator`.
- **Reference:** this repo sits exactly at PR #320's base, so the change set lines up 1:1 with that PR. Where a step says "PR-verbatim," copy the PR's content for that file.
- Commit immediately after each task (AO worktrees can be force-removed).

## File Structure

| File                                                                          | Status     | Responsibility                                                         |
| ----------------------------------------------------------------------------- | ---------- | ---------------------------------------------------------------------- |
| `backend/internal/legacyimport/orchestrator.go` (+`_test.go`)                 | delete     | orchestrator-session mapping/import (out of scope)                     |
| `backend/internal/legacyimport/claude.go` (+`_test.go`)                       | delete     | Claude transcript relocation (out of scope)                            |
| `backend/internal/storage/sqlite/store/session_import_store.go` (+`_test.go`) | delete     | `ImportSession` (only used by orchestrator import)                     |
| `backend/internal/legacyimport/importer.go`                                   | modify     | trim `Store`/`Options`/`Report`; drop orchestrator loop; add `quote()` |
| `backend/internal/legacyimport/config.go`                                     | modify     | `Repo` -> `*yaml.Node`; tolerate `*yaml.TypeError`                     |
| `backend/internal/legacyimport/paths.go`                                      | modify     | drop `defaultClaudeProjectsDir` + `projectSessionsDir`                 |
| `backend/internal/legacyimport/{importer,config,project}_test.go`             | modify     | projects-only assertions                                               |
| `backend/internal/cli/import.go`                                              | modify     | drop orchestrator/transcript copy + summary lines                      |
| `backend/internal/service/importer/importer.go` (+`_test.go`)                 | create     | `Status`/`Run` over the daemon store                                   |
| `backend/internal/httpd/controllers/imports.go` (+`_test.go`)                 | create     | `GET`/`POST /import`, 501 on nil svc                                   |
| `backend/internal/httpd/controllers/dto.go`                                   | modify     | `ImportStatusResponse`, `ImportRunResponse`                            |
| `backend/internal/httpd/apispec/specgen/build.go`                             | modify     | `import` tag + ops + schemaNames                                       |
| `backend/internal/httpd/api.go`                                               | modify     | `APIDeps.Import`, `API.imports`, wire + Register                       |
| `backend/internal/daemon/daemon.go`                                           | modify     | `Import: importsvc.New(...)`                                           |
| `backend/internal/cli/start.go`                                               | modify     | delete `maybeFirstBootImport` + 2 imports                              |
| `backend/internal/httpd/apispec/openapi.yaml`                                 | regenerate | —                                                                      |
| `frontend/src/api/schema.ts`                                                  | regenerate | —                                                                      |
| `frontend/src/shared/daemon-discovery.ts` (+`.test.ts`)                       | modify     | `shouldAdoptDiscoveredPort`                                            |
| `frontend/src/main.ts`                                                        | modify     | external daemon discovery loop                                         |
| `frontend/src/renderer/hooks/useImportStatus.ts`                              | create     | `useImportStatus`/`useRunImport`                                       |
| `frontend/src/renderer/components/ImportOffer.tsx` (+`.test.tsx`)             | create     | the banner                                                             |
| `frontend/src/renderer/components/SessionsBoard.tsx`                          | modify     | render `<ImportOffer/>`                                                |

---

## Task 1: Simplify the import engine to projects-only

**Files:** Delete `legacyimport/orchestrator.go`, `orchestrator_test.go`, `claude.go`, `claude_test.go`, `storage/sqlite/store/session_import_store.go`, `session_import_store_test.go`. Modify `legacyimport/importer.go`, `config.go`, `paths.go`, and their tests; `cli/import.go`.

**Interfaces:**

- Produces: trimmed `legacyimport.Store` (`GetProject`, `UpsertProject` only), `legacyimport.Options{Root, DryRun, Now, RepoOriginURL}`, `legacyimport.Report{DryRun, ProjectsImported, ProjectsSkipped, Notes}`. **Tasks 2 and 3 depend on this `Report` shape.**

- [ ] **Step 1: Delete the orchestrator/transcript files.**

```bash
git rm backend/internal/legacyimport/orchestrator.go backend/internal/legacyimport/orchestrator_test.go \
       backend/internal/legacyimport/claude.go backend/internal/legacyimport/claude_test.go \
       backend/internal/storage/sqlite/store/session_import_store.go \
       backend/internal/storage/sqlite/store/session_import_store_test.go
```

- [ ] **Step 2: Trim `legacyimport/importer.go`.**
  - `Store` interface: keep only `GetProject` and `UpsertProject` (drop `GetSession`, `ImportSession`).
  - `Options`: drop `DataDir` and `ClaudeProjectsDir`; keep `Root`, `DryRun`, `Now`, `RepoOriginURL`.
  - `Report`: reduce to `{DryRun bool, ProjectsImported int, ProjectsSkipped int, Notes []string}`.
  - In `Run`, delete the orchestrator block (the `sessionsDir`/`readOrchestratorMapping`/`switch mapping.status` that follows `importProject`), so the loop body ends after `importProject`.
  - Delete the `importOrchestrator` function.
  - Add the `quote()` helper used by the project-id note:

```go
// quote wraps s in double quotes for note messages, rendering an empty string as
// "?" so a missing value is still legible.
func quote(s string) string {
	if s == "" {
		return `"?"`
	}
	return `"` + s + `"`
}
```

- [ ] **Step 3: `legacyimport/config.go` parsing-robustness fix (PR-bundled, improves project import).**
  - Add `"errors"` import.
  - Change the project config's `Repo string \`yaml:"repo"\``field to`Repo \*yaml.Node \`yaml:"repo"\`` with a comment that it is captured but never consumed (origin is re-resolved from the repo path).
  - In `loadLegacyConfig`, when `yaml.Unmarshal` errors, keep the partial decode on a `*yaml.TypeError` instead of failing the whole registry:

```go
		var typeErr *yaml.TypeError
		if !errors.As(err, &typeErr) {
			return legacyConfig{}, fmt.Errorf("parse legacy config.yaml: %w", err)
		}
```

- [ ] **Step 4: `legacyimport/paths.go`.** Delete `defaultClaudeProjectsDir` and `projectSessionsDir`; drop the now-unused `"strings"` import; update the package doc to "maps the legacy project registry and per-project settings."
- [ ] **Step 5: Update tests.** Rewrite `importer_test.go` (drop `sessions` from `fakeStore`, drop orchestrator/transcript assertions, `writeLegacyRoot`/`runOpts` take only `root`), `config_test.go`, and `project_test.go` (add the `nonNilNode()` helper returning a populated `*yaml.Node`) per the PR.
- [ ] **Step 6: `cli/import.go`.** Update the command `Short`/`Long` to say "projects" only, the confirm prompt to "Import projects from %s?", and delete the `Orchestrators:`/`Transcripts:` lines from `writeImportSummary`. Remove the `DataDir` field from the `legacyimport.Options{...}` literal it builds.
- [ ] **Step 7:** `cd backend && go build ./... && go test ./internal/legacyimport/... ./internal/cli/... ./internal/storage/sqlite/...` → expect PASS.
- [ ] **Step 8:** Commit: `refactor(legacyimport): scope import to projects + settings only`

---

## Task 2: Import service (`service/importer`)

**Files:** Create `backend/internal/service/importer/importer.go`, `…/importer_test.go`

**Interfaces:**

- Consumes: trimmed `legacyimport.Store`, `legacyimport.Run`, `legacyimport.HasLegacyData`, `legacyimport.DefaultLegacyRootDir`, `legacyimport.Report`, `legacyimport.Options{Root}`; `domain.ProjectRecord`.
- Produces: `importer.Status{Available bool; LegacyRoot string}`, `importer.Service` (`Status(ctx)`, `Run(ctx)`), `importer.Deps{Store, Root}`, `importer.New(Deps) *Manager`. **Controller (Task 3) and daemon (Task 5) depend on these exact names.**

- [ ] **Step 1: Write `importer.go`** (projects-only: no `DataDir`):

```go
// Package importer is the controller-facing service for the legacy-AO import.
// It wraps the internal/legacyimport engine with the two operations the
// dashboard needs: a detection probe ("is a legacy install available?") and a
// trigger that runs the import through the live daemon's store, so the daemon
// stays the sole writer. The engine is reused verbatim; this package adds no
// import logic of its own, only the daemon-side detection and the store wiring.
package importer

import (
	"context"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/legacyimport"
)

// Store is the storage slice the import service needs: the legacy importer's
// write surface plus a project listing for the "already imported" check.
// *sqlite.Store satisfies it, so the daemon passes its single shared store and
// the import runs through the same write path as every other mutation.
type Store interface {
	legacyimport.Store
	ListProjects(ctx context.Context) ([]domain.ProjectRecord, error)
}

// Status reports whether a legacy AO install is available to import. Available
// is true only when legacy data is present AND the rewrite database holds no
// projects yet (the first-boot condition); a populated database is assumed
// already imported (or started fresh on purpose), so the offer is not surfaced.
type Status struct {
	Available  bool   `json:"available"`
	LegacyRoot string `json:"legacyRoot"`
}

// Service is the controller-facing import contract.
type Service interface {
	Status(ctx context.Context) (Status, error)
	Run(ctx context.Context) (legacyimport.Report, error)
}

// Deps bundles the import service's dependencies.
type Deps struct {
	// Store is the rewrite's durable store (the daemon's shared *sqlite.Store).
	Store Store
	// Root overrides the legacy AO root to read. Empty -> the default.
	Root string
}

// Manager implements Service over the daemon's store and config.
type Manager struct {
	store Store
	root  string
}

var _ Service = (*Manager)(nil)

// New constructs the import service. An empty Root falls back to the default
// legacy root so callers that don't override it get the standard location.
func New(deps Deps) *Manager {
	root := deps.Root
	if root == "" {
		root = legacyimport.DefaultLegacyRootDir()
	}
	return &Manager{store: deps.Store, root: root}
}

// Status reports import availability without touching legacy or rewrite data
// beyond a project count. It never errors on a missing legacy store; that is
// simply "not available".
func (m *Manager) Status(ctx context.Context) (Status, error) {
	st := Status{LegacyRoot: m.root}
	if !legacyimport.HasLegacyData(m.root) {
		return st, nil
	}
	projects, err := m.store.ListProjects(ctx)
	if err != nil {
		return Status{}, err
	}
	st.Available = len(projects) == 0
	return st, nil
}

// Run executes the import through the daemon's store. It is idempotent: the
// engine skips rows that already exist, so a re-run (or a run against a
// partially-populated database) is safe and never overwrites. Legacy files are
// never modified.
func (m *Manager) Run(ctx context.Context) (legacyimport.Report, error) {
	return legacyimport.Run(ctx, m.store, legacyimport.Options{Root: m.root})
}
```

- [ ] **Step 2: Write `importer_test.go`** PR-verbatim: `fakeStore` implements only `GetProject`/`UpsertProject`/`ListProjects` (the trimmed `Store` needs nothing else); tests `TestStatus_NoLegacyData`, `TestStatus_LegacyPresentEmptyDB`, `TestStatus_AlreadyPopulated`, `TestStatus_ListError`, `TestRun_ImportsThenStatusFlipsUnavailable`, `TestNew_DefaultsRoot`.
- [ ] **Step 3:** `cd backend && go test ./internal/service/importer/...` → expect PASS.
- [ ] **Step 4:** Commit: `feat(importer): import service over daemon store (status + run)`

---

## Task 3: HTTP controller + DTOs

**Files:** Create `backend/internal/httpd/controllers/imports.go`, `…/imports_test.go`; Modify `…/controllers/dto.go`

**Interfaces:**

- Consumes: `importsvc.Status`, `legacyimport.Report`, `apispec.NotImplemented`, `envelope.WriteJSON`/`WriteError`, `httpd.NewRouterWithControl(cfg, log, termMgr, APIDeps, ControlDeps)`.
- Produces: `controllers.ImportService`, `controllers.ImportController{Svc}`, `controllers.ImportStatusResponse`, `controllers.ImportRunResponse`. **api.go (Task 5) and specgen (Task 4) depend on these.**

- [ ] **Step 1:** Add to `dto.go` (add the `legacyimport` import):

```go
// ImportStatusResponse is the body of GET /api/v1/import: whether a legacy AO
// install is available to import, and the root the daemon would read from.
type ImportStatusResponse struct {
	Available  bool   `json:"available"`
	LegacyRoot string `json:"legacyRoot"`
}

// ImportRunResponse is the body of POST /api/v1/import: the structured outcome
// of the import run (counts + notes), reused verbatim from the import engine.
type ImportRunResponse struct {
	Report legacyimport.Report `json:"report"`
}
```

- [ ] **Step 2:** Create `imports.go` PR-verbatim (`ImportService` interface, `ImportController`, `Register`, `status`, `run`; nil `Svc` answers `apispec.NotImplemented`).
- [ ] **Step 3:** Create `imports_test.go` PR-verbatim (`fakeImportService`, `newImportTestServer` using `httpd.NewRouterWithControl(config.Config{}, log, nil, httpd.APIDeps{Import: svc}, httpd.ControlDeps{})`, tests `TestImportAPI_Status/StatusError/Run/RunError`). The `doRequest` helper already exists in the `controllers_test` package.
- [ ] **Step 4:** `go build ./...` fails until Task 5 wires `APIDeps.Import` (expected). Parse-check now: `gofmt -l internal/httpd/controllers/imports.go`. Test gate runs at end of Task 5.
- [ ] **Step 5:** Commit: `feat(httpd): import controller + DTOs (GET/POST /api/v1/import)`

---

## Task 4: OpenAPI spec generation

**Files:** Modify `backend/internal/httpd/apispec/specgen/build.go`; regenerate `openapi.yaml` + `frontend/src/api/schema.ts`

- [ ] **Step 1:** In `build.go` add, PR-verbatim: the `import` tag (in `Tags`), the schemaName mappings (`ControllersImportStatusResponse`->`ImportStatusResponse`, `ControllersImportRunResponse`->`ImportRunResponse`, `LegacyimportReport`->`ImportReport`), the `importOperations()` func (2 ops), and `ops = append(ops, importOperations()...)`.
- [ ] **Step 2:** Regenerate: `npm run api:spec` then `npm run api:ts`.
- [ ] **Step 3:** Inspect the diff. The generated `ImportReport` schema must be the projects-only 4-field shape (`dryRun`, `projectsImported`, `projectsSkipped`, `notes`), matching the PR's `schema.ts` verbatim. If orchestrator fields appear, Task 1's `Report` trim was incomplete; fix it before continuing.
- [ ] **Step 4:** `cd backend && go test ./internal/httpd/apispec/...` (route<->spec parity) → expect PASS.
- [ ] **Step 5:** Commit: `feat(apispec): describe /api/v1/import; regenerate openapi + schema.ts`

---

## Task 5: Wire controller into API + daemon

**Files:** Modify `backend/internal/httpd/api.go`, `backend/internal/daemon/daemon.go`

- [ ] **Step 1:** `api.go` edits:
  - `APIDeps`: add `Import controllers.ImportService` (after `NotificationStream`).
  - `API` struct: add `imports *controllers.ImportController` (after `notifications`).
  - `NewAPI`: add `imports: &controllers.ImportController{Svc: deps.Import},` (after the `notifications:` line).
  - `Register` timeout group: add `a.imports.Register(r)` (after `a.notifications.Register(r)`).
- [ ] **Step 2:** `daemon.go` edits:
  - Add import: `importsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/importer"`.
  - In the `httpd.APIDeps{...}` literal, add: `Import: importsvc.New(importsvc.Deps{Store: store}),` (projects-only: **no** `DataDir`).
- [ ] **Step 3:** `cd backend && go build ./... && go test ./internal/httpd/... ./internal/service/importer/...` → expect PASS (gate for Task 3's tests too).
- [ ] **Step 4:** Commit: `feat(daemon): mount import service on the API`

---

## Task 6: Make `ao start` headless

**Files:** Modify `backend/internal/cli/start.go`

- [ ] **Step 1:** Delete the `maybeFirstBootImport` method and its call. Replace the call + the comment above it with:

```go
	// `ao start` is headless: it only launches the daemon. Detecting a legacy AO
	// install and offering to import it is the dashboard's job (it polls
	// GET /api/v1/import and POSTs to run the import through the live daemon).
	// `ao import` remains for explicit offline imports.
```

- [ ] **Step 2:** Remove now-unused imports `…/internal/legacyimport` and `…/internal/storage/sqlite`. Keep `config`. Leave `cli/import.go`, `confirm`, `stdinIsInteractive`, `writeImportSummary` untouched.
- [ ] **Step 3:** `cd backend && go build ./... && go test ./internal/cli/...` → expect PASS.
- [ ] **Step 4:** Commit: `refactor(cli): ao start no longer prompts; import moves to the dashboard`

---

## Task 7: External daemon discovery (Electron main)

**Files:** Modify `frontend/src/shared/daemon-discovery.ts`, `…/daemon-discovery.test.ts`, `frontend/src/main.ts`

**Interfaces:** Produces `shouldAdoptDiscoveredPort(current: DaemonStatus, discoveredPort: number): boolean`.

- [ ] **Step 1 (TDD):** Add the `shouldAdoptDiscoveredPort` describe block to `daemon-discovery.test.ts` (PR-verbatim). Run `npm --prefix frontend run test -- daemon-discovery` → expect FAIL (not exported).
- [ ] **Step 2:** Add `shouldAdoptDiscoveredPort` + the `DaemonStatus` type import to `daemon-discovery.ts` (PR-verbatim). Re-run → expect PASS.
- [ ] **Step 3:** In `main.ts`: import `shouldAdoptDiscoveredPort`, add `EXTERNAL_DISCOVERY_POLL_MS = 1_000`, `externalDiscoveryTimer`, `discoverExternalDaemonOnce()`, `startExternalDaemonDiscovery()` (PR-verbatim); call `startExternalDaemonDiscovery()` in `app.whenReady().then(...)` and clear the timer in `before-quit`. Skip the prettier-only reflows the PR carries.
- [ ] **Step 4:** `npm --prefix frontend run typecheck` → expect PASS.
- [ ] **Step 5:** Commit: `feat(electron): discover an externally-started daemon from running.json`

---

## Task 8: Import status hooks

**Files:** Create `frontend/src/renderer/hooks/useImportStatus.ts`

**Interfaces:** Consumes `apiClient`/`apiErrorMessage` (`renderer/lib/api-client`), `workspaceQueryKey` (`renderer/hooks/useWorkspaceQuery`, value `["workspaces"]`). Produces `useImportStatus()`, `useRunImport()`, `importStatusQueryKey`, types `ImportStatus`/`ImportReport`.

- [ ] **Step 1:** Create the file PR-verbatim (`ImportReport` TS type = `{projectsImported, projectsSkipped, notes?}`). `useImportStatus` polls every 30s with `throwOnError: false`; `useRunImport` invalidates `importStatusQueryKey` + `workspaceQueryKey` on success.
- [ ] **Step 2:** `npm --prefix frontend run typecheck` → expect PASS.
- [ ] **Step 3:** Commit: `feat(renderer): useImportStatus / useRunImport hooks`

---

## Task 9: ImportOffer banner

**Files:** Create `frontend/src/renderer/components/ImportOffer.tsx`, `…/ImportOffer.test.tsx`

- [ ] **Step 1 (TDD):** Create `ImportOffer.test.tsx` PR-verbatim (mocks `../lib/api-client`; asserts offer-shown/hidden/accept/decline/error). Run `npm --prefix frontend run test -- ImportOffer` → expect FAIL (component missing).
- [ ] **Step 2:** Create `ImportOffer.tsx` PR-verbatim. Heading "Import projects from your earlier AO?"; body copy "Importing brings in your projects. Your old files are never modified, and you can do this later instead." (projects-only, no orchestrator mention). Built from `ui/button` (`primary`/`ghost`, size `sm`). Re-run → expect PASS.
- [ ] **Step 3:** Commit: `feat(renderer): ImportOffer dashboard banner`

---

## Task 10: Render the banner on the board

**Files:** Modify `frontend/src/renderer/components/SessionsBoard.tsx`

- [ ] **Step 1:** Add `import { ImportOffer } from "./ImportOffer";` and, directly under the `<DashboardSubhead .../>` line, insert:

```tsx
{
	/* First-run legacy-AO import opt-in. Renders only when the daemon
			    reports an importable install, and only on the top-level board. */
}
{
	!projectId && <ImportOffer />;
}
```

- [ ] **Step 2:** `npm --prefix frontend run typecheck && npm --prefix frontend run test` → expect PASS.
- [ ] **Step 3:** Commit: `feat(renderer): surface ImportOffer on the dashboard board`

---

## Task 11: Full verification

- [ ] `cd backend && go build ./... && go test -race ./...` → green.
- [ ] `golangci-lint run` on touched packages → clean.
- [ ] `npm --prefix frontend run typecheck && npm --prefix frontend run test` → green.
- [ ] Confirm `git status` shows **no** uncommitted drift in `openapi.yaml`/`schema.ts` after a fresh `npm run api:spec && npm run api:ts`.
- [ ] **Full build** (per the build-verification rule; rollup tree-shaking can hide missing emits): run the frontend production build.
- [ ] `ao preview` the dashboard against a daemon pointed at a seeded `~/.agent-orchestrator` legacy root with an empty rewrite DB; verify the banner appears, Import imports the projects + retires the banner, Not now dismisses.

---

## Self-Review

**Spec coverage:** ✅ engine simplified to projects-only (T1); `service/importer` status+run (T2); `GET`/`POST /api/v1/import` + 501 (T3); OpenAPI + schema.ts regen (T4); api/daemon wiring (T5); headless `ao start` (T6); external daemon discovery (T7); `useImportStatus`/`useRunImport` (T8); `ImportOffer` + board render (T9/T10). Matches PR #320 1:1.

**Placeholder scan:** none — every new file has full source or is "PR-verbatim" for an existing PR file; every edit names the symbol and location.

**Type consistency:** `importer.Deps{Store, Root}` (T2) ↔ daemon `importsvc.New(importsvc.Deps{Store: store})` (T5) ✅ (no `DataDir` anywhere). `legacyimport.Report` 4-field shape (T1) ↔ `ImportRunResponse` (T3) ↔ generated `ImportReport` (T4) ↔ `ImportReport` TS type (T8) ✅. `controllers.ImportService` (T3) ↔ `APIDeps.Import` (T5) ✅. `importStatusQueryKey`/`workspaceQueryKey` (T8) ↔ `ImportOffer` (T9) ✅. `Status{Available, LegacyRoot}` consistent across service/controller/DTO/schema ✅.

**Cross-task coupling to watch:** (a) Task 3's controller test only goes green after Task 5 wires `APIDeps.Import` (gate = end of Task 5); (b) Task 4 must run after `dto.go` (Task 3) exists so the reflector sees the new types; (c) Task 4 Step 3 is the guard that Task 1's `Report` trim actually took.
