# One-Click Vendor Installers Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make first-party remote installer scripts a one-click automatic fallback for every natively supported harness in PR #4221.

**Architecture:** The daemon keeps complete authority over harness IDs, URLs, interpreters, and execution policy. `systeminstall` selects a typed remote-script recipe, while `systemexec` downloads the complete HTTPS response into an AO-owned private directory and executes the saved file with closed stdin; the existing durable job and adapter verifier remain the source of installation state.

**Tech Stack:** Go 1.25, `net/http`, `crypto/sha256`, Electron/React, Vitest, SQLite-backed durable install jobs.

**Spec:** `docs/superpowers/specs/2026-09-01-one-click-vendor-installers-design.md`

## Global Constraints

- Clicking **Install** or **Reinstall** is the only confirmation; do not add a second dialog.
- The renderer submits only a fixed harness ID and method ID. It never submits a URL, interpreter, script body, argv, or environment.
- Only exact HTTPS URLs registered in Go source may execute; every redirect must remain HTTPS and the redirect count is bounded.
- Download the entire bounded response before execution. Never use `curl | shell`, `irm | iex`, `shell -c`, or PowerShell `-Command`.
- Store temporary scripts only beneath `<AO_DATA_DIR>/installers/tmp`; use `0700` job directories and `0600` script files on Unix, and remove them after every outcome.
- Keep package managers preferred when viable. The official script is an automatic fallback and a selectable alternate method.
- Never use `sudo`, interactive stdin, automatic retries, or bulk install-at-startup behavior.
- Preserve the existing `installing -> verifying -> succeeded|failed` lifecycle, interrupted-job recovery, Droid activity gate, bounded output, and adapter-backed version verification.
- Keep WSL-only or unsupported native Windows targets instruction-only.
- Do not install or reinstall a real third-party harness during automated or live verification.

---

### Task 1: Add the bounded remote-script execution port

**Files:**
- Modify: `backend/internal/ports/system.go`
- Modify: `backend/internal/adapters/systemexec/systemexec.go`
- Create: `backend/internal/adapters/systemexec/remote_script.go`
- Create: `backend/internal/adapters/systemexec/remote_script_test.go`
- Modify: `backend/internal/adapters/systemexec/systemexec_unix_test.go`

**Interfaces:**
- Consumes: the existing `ports.InstallCommand`, `Adapter.RunInstall`, process-group cancellation helpers, and AO data directory supplied later by daemon wiring.
- Produces: `ports.InstallScriptCommand`, `ports.InstallScriptResult`, `ports.InstallScriptRunner`, `systemexec.New(dataDir string) Adapter`, and `Adapter.RunInstallScript(context.Context, ports.InstallScriptCommand, io.Writer, io.Writer) (ports.InstallScriptResult, error)`.

- [ ] **Step 1: Define the failing port and adapter tests**

Add these types to `backend/internal/ports/system.go`:

```go
type InstallScriptCommand struct {
	URL         string
	Interpreter []string
	Env         []string
}

type InstallScriptResult struct {
	SHA256 string
}

type InstallScriptRunner interface {
	RunInstallScript(ctx context.Context, command InstallScriptCommand, stdout, stderr io.Writer) (InstallScriptResult, error)
}
```

Create `remote_script_test.go` with an `httptest.NewTLSServer` whose body is:

```sh
#!/bin/sh
printf 'vendor-script-ran'
```

Construct the test adapter with an unexported `newAdapter(dataDir string, client *http.Client)` seam, call:

```go
result, err := adapter.RunInstallScript(context.Background(), ports.InstallScriptCommand{
	URL: server.URL, Interpreter: []string{"sh"}, Env: []string{"CI=1"},
}, &output, &output)
```

Assert all of the following:

```go
if err != nil { t.Fatal(err) }
if output.String() != "vendor-script-ran" { t.Fatalf("output = %q", output.String()) }
if result.SHA256 != hex.EncodeToString(sum[:]) { t.Fatalf("sha256 = %q", result.SHA256) }
entries, err := os.ReadDir(filepath.Join(dataDir, "installers", "tmp"))
if err != nil || len(entries) != 0 { t.Fatalf("temporary scripts remain: %v, %v", entries, err) }
```

Add `TestNewUsesAODataDir` and assert `New(dataDir).installerRoot` equals
`filepath.Join(dataDir, "installers", "tmp")`; this is the regression guard
for AO's data-directory boundary used by production daemon wiring.

Add table cases proving an `http://` initial URL, an HTTPS redirect to HTTP, more than five redirects, a non-2xx response, and a response larger than `4 << 20` bytes return errors without invoking the interpreter. Add a canceled-context case that blocks the server response until `ctx.Done()` and expects `context.Canceled`.

In `systemexec_unix_test.go`, add a fixture script that reports EOF from stdin and checks its own mode and parent-directory mode using portable `stat` fallbacks. Assert the script observes closed stdin, file mode `600`, directory mode `700`, and the supplied noninteractive environment.

- [ ] **Step 2: Run the adapter tests and verify RED**

Run:

```bash
cd backend
go test ./internal/adapters/systemexec -run 'TestRunInstallScript|TestRunInstallScriptUnixPolicy' -count=1
```

Expected: compilation fails because the script port, adapter constructor, and `RunInstallScript` do not exist.

- [ ] **Step 3: Implement the downloader and private execution boundary**

Change `Adapter` to hold its AO-owned installer root and HTTP client while preserving zero-value behavior for non-script methods:

```go
type Adapter struct {
	installerRoot string
	httpClient    *http.Client
}

func New(dataDir string) Adapter {
	return newAdapter(dataDir, http.DefaultClient)
}

func newAdapter(dataDir string, client *http.Client) Adapter {
	return Adapter{
		installerRoot: filepath.Join(dataDir, "installers", "tmp"),
		httpClient: client,
	}
}
```

In `remote_script.go`, use exact constants:

```go
const (
	remoteScriptDownloadTimeout = 30 * time.Second
	remoteScriptMaxBytes        = 4 << 20
	remoteScriptMaxRedirects    = 5
)
```

Implement `RunInstallScript` in this order:

1. Reject an empty installer root, empty interpreter, malformed URL, or any scheme other than `https`.
2. Derive a 30-second download context from the install context.
3. Clone the configured HTTP client and install `CheckRedirect` that rejects more than five redirects or any non-HTTPS destination.
4. require `200 <= StatusCode < 300`.
5. Read through `io.LimitReader(response.Body, remoteScriptMaxBytes+1)` and reject a body longer than `remoteScriptMaxBytes`.
6. Compute `sha256.Sum256(body)` and return its lowercase hex digest even when later execution fails.
7. `os.MkdirAll(installerRoot, 0o700)`, `os.Chmod(installerRoot, 0o700)`, then `os.MkdirTemp(installerRoot, "job-*")` and `os.Chmod(jobDir, 0o700)`.
8. Select `.ps1` when the interpreter executable contains `powershell` or `pwsh` case-insensitively; otherwise select `.sh`. Write `installer+extension` with mode `0600` and enforce `os.Chmod(scriptPath, 0o600)`.
9. Call `RunInstall` with `Argv: append(copy(command.Interpreter), scriptPath)` and `Env: command.Env`.
10. Remove the job directory and combine execution and cleanup errors with `errors.Join`, so cleanup never hides the installer failure.

Do not print or persist the script body. Do not construct a shell expression.

- [ ] **Step 4: Run adapter tests and package tests**

Run:

```bash
cd backend
go test ./internal/adapters/systemexec -count=1
```

Expected: PASS, including cancellation killing the process group and all new HTTPS/download/filesystem cases.

- [ ] **Step 5: Commit the execution boundary**

```bash
git add backend/internal/ports/system.go backend/internal/adapters/systemexec/systemexec.go backend/internal/adapters/systemexec/remote_script.go backend/internal/adapters/systemexec/remote_script_test.go backend/internal/adapters/systemexec/systemexec_unix_test.go
git commit -m "feat: add bounded vendor script runner"
```

---

### Task 2: Convert script-only plans into automatic server-owned methods

**Files:**
- Modify: `backend/internal/service/systeminstall/systeminstall.go`
- Modify: `backend/internal/service/systeminstall/agentplans.go`
- Modify: `backend/internal/service/systeminstall/agentplans_test.go`

**Interfaces:**
- Consumes: `ports.InstallScriptCommand` from Task 1 and existing package-manager `Plan` recipes.
- Produces: `Plan.Script *ports.InstallScriptCommand`, method ID `official-installer`, label `Official installer`, exact first-party URL/interpreter recipes, and package-manager-first method lists with script fallback.

- [ ] **Step 1: Replace obsolete safety tests with failing one-click recipe tests**

Delete `TestAgentPlansNeverAutoExecuteRemoteScriptsOrSudo` and `TestScriptOnlyHarnessesAreManual`. Replace them with a table-driven `TestOfficialInstallerPlansAreAutomaticAndServerOwned` covering these exact native recipes:

```go
tests := []struct {
	goos        string
	target      Target
	found       []string
	wantURL     string
	wantProgram string
}{
	{"darwin", TargetCursor, []string{"bash"}, "https://cursor.com/install", "bash"},
	{"windows", TargetCursor, []string{"pwsh.exe"}, "https://cursor.com/install?win32=true", "pwsh.exe"},
	{"linux", TargetAider, []string{"sh"}, "https://aider.chat/install.sh", "sh"},
	{"linux", TargetGrok, []string{"bash"}, "https://x.ai/cli/install.sh", "bash"},
	{"linux", TargetKimi, []string{"bash"}, "https://code.kimi.com/kimi-code/install.sh", "bash"},
	{"linux", TargetGoose, []string{"bash"}, "https://github.com/aaif-goose/goose/releases/download/stable/download_cli.sh", "bash"},
	{"linux", TargetDevin, []string{"bash"}, "https://cli.devin.ai/install.sh", "bash"},
	{"windows", TargetKiro, []string{"powershell.exe"}, "https://cli.kiro.dev/install.ps1", "powershell.exe"},
	{"linux", TargetMuse, []string{"bash"}, "https://dev.meta.ai/install.sh", "bash"},
	{"windows", TargetAgy, []string{"pwsh"}, "https://antigravity.google/cli/install.ps1", "pwsh"},
	{"linux", TargetPrimeAgent, []string{"sh"}, "https://app.primeintellect.ai/prime-agent/install.sh", "sh"},
}
```

For each case assert:

```go
plan := newTestService(tt.goos, tt.found...).planAgent(tt.target)
if plan.Unsupported || plan.Method != "official-installer" || plan.Script == nil {
	t.Fatalf("plan = %+v", plan)
}
if plan.Script.URL != tt.wantURL || plan.Script.Interpreter[0] != "/usr/bin/"+tt.wantProgram {
	t.Fatalf("script = %+v", plan.Script)
}
if len(plan.Command) != 0 { t.Fatalf("remote plan exposed executable argv: %v", plan.Command) }
```

Add `TestOfficialInstallerRequiresInterpreter` and expect an unavailable `official-installer` plan with a reason containing `was not found on PATH` when its fixed interpreter cannot resolve.

Add `TestPackageManagerMethodsStayPreferredBeforeOfficialInstaller`: with writable Homebrew/npm capabilities and `bash`, Codex must list `homebrew`, `npm`, then `official-installer`; Homebrew alone is recommended. With Homebrew/npm unavailable and `bash` present, `official-installer` must be recommended and available.

Keep an explicit no-sudo/no-shell-evaluation assertion over every `Plan.Command` and every `Plan.Script.Interpreter`: no element may equal `sudo`, `-c`, or `-Command`.

Keep Windows instruction-only assertions for Goose, Devin, Muse, and Prime Agent.

- [ ] **Step 2: Run the recipe tests and verify RED**

Run:

```bash
cd backend
go test ./internal/service/systeminstall -run 'TestOfficialInstaller|TestPackageManagerMethodsStayPreferred' -count=1
```

Expected: compilation fails because `Plan.Script` does not exist, then behavioral failures show script plans are still manual.

- [ ] **Step 3: Add typed remote recipes and method ordering**

Extend `Plan`:

```go
Script *ports.InstallScriptCommand
```

Change `planShellInstaller` to resolve its fixed interpreter through `LookPath` and return:

```go
return Plan{
	Target: target,
	Method: "official-installer",
	Script: &ports.InstallScriptCommand{
		URL: url,
		Interpreter: []string{resolvedShell},
	},
}
```

Change `planPowerShellInstaller` to resolve the first available executable in this order:

```go
[]string{"pwsh.exe", "powershell.exe", "pwsh", "powershell"}
```

Its interpreter argv must be:

```go
[]string{resolvedPowerShell, "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File"}
```

The runner appends the temporary `.ps1` path after `-File`.

Update `displayCommand`:

```go
if plan.Script != nil {
	return fmt.Sprintf("%s <downloaded from %s>", strings.Join(plan.Script.Interpreter, " "), plan.Script.URL)
}
```

Return `Official installer` from `installMethodLabel("official-installer")`.

Update `requestPlanner.agentMethodPlans` so viable package managers remain first and the first-party script is appended last. For script-only native targets, return only `planAgent(target)`. Add official fallback entries for Claude Code, Codex, OpenCode on Unix, Pi on Unix, Amp, Droid, Qwen, Autohand, and OMP wherever first-party native URLs exist. Preserve existing manual Windows plans for Goose, Devin, Muse, and Prime Agent.

Use this complete URL registry; do not discover URLs dynamically:

| Harness | Unix URL / shell | Native Windows URL |
|---|---|---|
| Claude Code | `https://claude.ai/install.sh` / `bash` | `https://claude.ai/install.ps1` |
| Codex | `https://chatgpt.com/codex/install.sh` / `sh` | `https://chatgpt.com/codex/install.ps1` |
| Cursor | `https://cursor.com/install` / `bash` | `https://cursor.com/install?win32=true` |
| OpenCode | `https://opencode.ai/install` / `bash` | none; keep winget |
| Aider | `https://aider.chat/install.sh` / `sh` | `https://aider.chat/install.ps1` |
| Grok | `https://x.ai/cli/install.sh` / `bash` | `https://x.ai/cli/install.ps1` |
| Kimi | `https://code.kimi.com/kimi-code/install.sh` / `bash` | `https://code.kimi.com/kimi-code/install.ps1` |
| Pi | `https://pi.dev/install.sh` / `sh` | none; keep npm |
| Amp | `https://ampcode.com/install.sh` / `bash` | `https://ampcode.com/install.ps1` |
| Droid | `https://app.factory.ai/cli` / `sh` | `https://app.factory.ai/cli/windows` |
| Goose | `https://github.com/aaif-goose/goose/releases/download/stable/download_cli.sh` / `bash` | none; keep WSL/desktop instructions |
| Qwen | `https://qwen-code-assets.oss-cn-hangzhou.aliyuncs.com/installation/install-qwen-standalone.sh` / `bash` | `https://qwen-code-assets.oss-cn-hangzhou.aliyuncs.com/installation/install-qwen-standalone.ps1` |
| Devin | `https://cli.devin.ai/install.sh` / `bash` | none; keep WSL instructions |
| Kiro | `https://cli.kiro.dev/install` / `bash` | `https://cli.kiro.dev/install.ps1` |
| Muse | `https://dev.meta.ai/install.sh` / `bash` | none; keep unsupported instructions |
| AGY | `https://antigravity.google/cli/install.sh` / `bash` | `https://antigravity.google/cli/install.ps1` |
| Autohand | `https://autohand.ai/install.sh` / `sh` | `https://autohand.ai/install.ps1` |
| Kimchi | `https://github.com/getkimchi/kimchi/releases/latest/download/install.sh` / `sh` | `https://github.com/getkimchi/kimchi/releases/latest/download/install.ps1` |
| Prime Agent | `https://app.primeintellect.ai/prime-agent/install.sh` / `sh` | none; keep WSL instructions |
| OMP | `https://omp.sh/install` / `sh` | `https://omp.sh/install.ps1` |

- [ ] **Step 4: Run recipe and service tests**

Run:

```bash
cd backend
go test ./internal/service/systeminstall -count=1
```

Expected: PASS. Every available plan has a display command and stable method, package methods remain preferred, and script-only native targets are automatic.

- [ ] **Step 5: Commit the recipe registry**

```bash
git add backend/internal/service/systeminstall/systeminstall.go backend/internal/service/systeminstall/agentplans.go backend/internal/service/systeminstall/agentplans_test.go
git commit -m "feat: expose official harness installers"
```

---

### Task 3: Execute script recipes through durable harness jobs

**Files:**
- Modify: `backend/internal/service/systeminstall/systeminstall.go`
- Modify: `backend/internal/service/systeminstall/systeminstall_test.go`
- Modify: `backend/internal/daemon/daemon.go`

**Interfaces:**
- Consumes: `ports.InstallScriptRunner.RunInstallScript` from Task 1 and `Plan.Script` from Task 2.
- Produces: script-aware `Service.runAgentInstall`, SHA-256/source diagnostics, and production `systemexec.New(cfg.DataDir)` wiring.

- [ ] **Step 1: Add failing durable-job tests**

In `systeminstall_test.go`, define:

```go
type installScriptRunnerFunc func(context.Context, ports.InstallScriptCommand, io.Writer, io.Writer) (ports.InstallScriptResult, error)

func (f installScriptRunnerFunc) RunInstallScript(ctx context.Context, command ports.InstallScriptCommand, stdout, stderr io.Writer) (ports.InstallScriptResult, error) {
	return f(ctx, command, stdout, stderr)
}
```

Add `TestAgentVendorScriptInstallPersistsInstallVerifySuccessLifecycle`. Configure a Linux Cursor service with `bash`, a durable fake store, a script runner that captures the command and writes `installed\n`, and a verifier returning `/home/test/.local/bin/agent` plus `cursor 1.2.3\n`. Start method `official-installer`, wait for success, then assert:

```go
if captured.URL != "https://cursor.com/install" { t.Fatalf("URL = %q", captured.URL) }
if final.Method != "official-installer" { t.Fatalf("method = %q", final.Method) }
if !strings.Contains(final.Output, "source: https://cursor.com/install") ||
	!strings.Contains(final.Output, "sha256: abc123") ||
	!strings.Contains(final.Output, "cursor 1.2.3") {
	t.Fatalf("output = %q", final.Output)
}
if strings.Join(statuses, ",") != "installing,verifying,succeeded" { t.Fatalf("statuses = %v", statuses) }
```

Add failure cases for missing `InstallScriptRunner`, runner error after returning a digest, timeout, and daemon cancellation. Each must end in `failed` or `interrupted` consistently with the existing package-manager behavior and must never invoke the verifier after runner failure.

- [ ] **Step 2: Run durable job and wiring tests and verify RED**

Run:

```bash
cd backend
go test ./internal/service/systeminstall -run 'TestAgentVendorScript' -count=1
```

Expected: compilation fails because `Service` has no script runner.

- [ ] **Step 3: Route remote recipes through the dedicated runner**

Add this field to `Service`:

```go
installScripts ports.InstallScriptRunner
```

In `NewWithDeps`, populate it with a type assertion from the shared command adapter, just as `InstallCommandRunner` is populated:

```go
installScripts, _ := commands.(ports.InstallScriptRunner)
```

In `runAgentInstall`, keep the existing controlled environment in one local `env` slice. Branch by recipe type:

```go
if plan.Script != nil {
	if s.installScripts == nil {
		runErr = errors.New("remote installer runner is not configured")
	} else {
		command := *plan.Script
		command.Env = append([]string(nil), env...)
		var result ports.InstallScriptResult
		result, runErr = s.installScripts.RunInstallScript(ctx, command, out, out)
		if result.SHA256 != "" {
			_, _ = fmt.Fprintf(out, "\nsource: %s\nsha256: %s\n", command.URL, result.SHA256)
		}
	}
} else if s.installCommands != nil {
	runErr = s.installCommands.RunInstall(ctx, ports.InstallCommand{Argv: plan.Command, Env: env}, out, out)
} else {
	runErr = s.commands.Run(ctx, plan.Command, out, out)
}
```

Keep the existing timeout/cancellation/failure handling immediately after this branch, followed by verifying state and adapter verification.

In daemon wiring, replace:

```go
hostCommands := systemexec.Adapter{}
```

with:

```go
hostCommands := systemexec.New(cfg.DataDir)
```

Do not add a new HTTP field or route.

- [ ] **Step 4: Run backend service, daemon, controller, and API parity tests**

Run:

```bash
cd backend
go test ./internal/service/systeminstall ./internal/adapters/systemexec ./internal/daemon ./internal/httpd/controllers ./internal/httpd/apispec ./internal/httpd -count=1
```

Expected: PASS. No OpenAPI regeneration should be required because the wire contract is unchanged.

- [ ] **Step 5: Commit durable execution integration**

```bash
git add backend/internal/service/systeminstall/systeminstall.go backend/internal/service/systeminstall/systeminstall_test.go backend/internal/daemon/daemon.go
git commit -m "feat: run vendor installers as durable jobs"
```

---

### Task 4: Prove the one-click Settings behavior and update policy documentation

**Files:**
- Modify: `frontend/src/renderer/components/settings/HarnessSettingsSection.test.tsx`
- Modify: `docs/superpowers/plans/2026-08-31-safe-harness-installer-design.md`
- Modify: `docs/superpowers/plans/2026-08-31-safe-harness-installer.md`
- Modify: `docs/superpowers/specs/2026-09-01-one-click-vendor-installers-design.md` only if implementation details required a user-approved correction.

**Interfaces:**
- Consumes: unchanged `AgentInstallPlan` and install POST API, plus the `official-installer` method exposed by Task 2.
- Produces: renderer regression coverage and documentation consistent with the approved one-click trust model.

- [ ] **Step 1: Add renderer regression coverage**

Extend the test catalog with absent Cursor and Kiro rows. Return plans shaped like:

```ts
{
	agentId: "cursor", available: true, automatic: true,
	method: "official-installer",
	command: "/usr/bin/bash <downloaded from https://cursor.com/install>",
	documentationUrl: "https://docs.cursor.com/en/cli/installation",
	methods: [{
		id: "official-installer", label: "Official installer", available: true,
		recommended: true,
		command: "/usr/bin/bash <downloaded from https://cursor.com/install>",
	}],
}
```

Add `it("starts a script-only harness with one click and no confirmation", ...)`. Click Cursor's **Install** button once and assert the only mutation is:

```ts
expect(apiClient.POST).toHaveBeenCalledWith("/api/v1/agents/{agent}/install", {
	params: { path: { agent: "cursor" } },
	body: { method: "official-installer" },
});
expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
```

Add a separate fixture for native Windows Devin with `available: false`, `method: "manual"`, and a WSL reason; assert the row still shows **Instructions** and has no **Install** button.

- [ ] **Step 2: Run the renderer characterization test**

Run with the repository-supported Node version:

```bash
cd frontend
npm test -- src/renderer/components/settings/HarnessSettingsSection.test.tsx
```

Expected: PASS because the existing generic method UI already supports an available daemon-owned method. This is intentional characterization coverage: the production behavior changes in the backend recipe catalog, while the test prevents a future renderer confirmation dialog or client-provided command from being introduced. If the test exposes a real renderer mismatch, keep the production change limited to rendering the existing server-owned method fields and add `frontend/src/renderer/components/settings/HarnessSettingsSection.tsx` to this task's commit.

- [ ] **Step 3: Update the existing installer policy documents**

In `2026-08-31-safe-harness-installer-design.md`, replace:

```text
Mutable `curl | shell` recipes are not executed automatically.
```

with the approved policy: exact first-party HTTPS scripts are downloaded completely into AO-owned private storage and executed automatically after one Install click; package managers remain preferred; streamed shell pipelines remain prohibited.

In `2026-08-31-safe-harness-installer.md`, replace tests and checklist items that require script-only plans to be manual. Add explicit checklist items for HTTPS enforcement, bounded download/file permissions/cleanup, official-installer fallback selection, and one-click renderer coverage.

- [ ] **Step 4: Run UI tests and typecheck**

Run:

```bash
cd frontend
npm test -- src/renderer/components/settings/HarnessSettingsSection.test.tsx
npm run typecheck
```

Expected: PASS with no confirmation dialog and only `{ method: "official-installer" }` sent by the renderer.

- [ ] **Step 5: Commit UI coverage and policy documentation**

```bash
git add frontend/src/renderer/components/settings/HarnessSettingsSection.test.tsx docs/superpowers/plans/2026-08-31-safe-harness-installer-design.md docs/superpowers/plans/2026-08-31-safe-harness-installer.md
git commit -m "test: cover one-click vendor installers"
```

---

### Task 5: Full verification, desktop inspection, review, and PR update

**Files:**
- Verify all files changed by Tasks 1-4.
- Modify the PR #4221 body to describe the final one-click policy after the commit is pushed.

**Interfaces:**
- Consumes: the completed implementation and its tests.
- Produces: a reviewed, verified PR #4221 head with accurate documentation.

- [ ] **Step 1: Run formatting and generated-artifact drift checks**

```bash
gofmt -w backend/internal/ports/system.go backend/internal/adapters/systemexec/systemexec.go backend/internal/adapters/systemexec/remote_script.go backend/internal/adapters/systemexec/remote_script_test.go backend/internal/adapters/systemexec/systemexec_unix_test.go backend/internal/service/systeminstall/systeminstall.go backend/internal/service/systeminstall/agentplans.go backend/internal/service/systeminstall/agentplans_test.go backend/internal/service/systeminstall/systeminstall_test.go backend/internal/daemon/daemon.go
git diff --check
npm run api
git diff --exit-code -- backend/internal/httpd/apispec/openapi.yaml frontend/src/api/schema.ts
```

Expected: formatting succeeds, no whitespace errors, and generated API files have no drift because the HTTP contract did not change.

- [ ] **Step 2: Run the complete repository verification gates**

```bash
cd backend
go test ./...
go vet ./...
cd ../frontend
npm test -- src/renderer/components/settings/HarnessSettingsSection.test.tsx
npm run typecheck
npm run build
```

Expected: every command exits 0.

- [ ] **Step 3: Inspect the real Electron UI against isolated AO data**

Launch the desktop app using the `ao-desktop-dev` skill and isolated paths under `~/.ao/dev`. Open Settings → Harness and confirm:

- Cursor, Kiro, and other natively supported script-only rows show **Install** and method **Official installer**;
- one click is represented by a single Install button with no confirmation dialog;
- WSL-only Windows behavior is covered by automated tests, not guessed from macOS;
- existing progress, diagnostics, Verify again, and Reinstall controls still render.

Do not click a real vendor Install button. The adapter tests already execute harmless TLS fixture scripts.

- [ ] **Step 4: Request an independent code review**

Use `superpowers:requesting-code-review` with the pre-implementation commit as base and current HEAD as head. Require review of URL allowlisting, redirect policy, size/time bounds, filesystem permissions and cleanup, Windows argv, process-tree cancellation, durable job transitions, renderer authority, and all platform recipe mappings. Fix every Critical or Important finding and rerun the affected tests.

- [ ] **Step 5: Commit any review fixes and rerun the full gate**

```bash
git status --short
git add -u
git commit -m "fix: close vendor installer review gaps"
cd backend && go test ./...
cd ../frontend && npm run typecheck && npm test -- src/renderer/components/settings/HarnessSettingsSection.test.tsx
git diff --check
```

Expected: all verification commands exit 0 and the worktree contains no unrelated staged files. Leave `docs/superpowers/plans/2026-08-27-harness-authentication.md` untouched if it remains an unrelated untracked user file.

- [ ] **Step 6: Push and update PR #4221**

```bash
git push origin codex/harness-install-settings-main
gh pr edit 4221 --repo Untrivial-ai/agent-orchestrator --body-file /tmp/pr-4221-body.md
gh pr view 4221 --repo Untrivial-ai/agent-orchestrator --json url,headRefOid,statusCheckRollup
```

The updated PR body must state that Install is one click with no extra confirmation, package managers remain preferred, exact first-party HTTPS scripts are downloaded to AO-owned private files before execution, script contents remain mutable vendor-controlled code, and native-unsupported Windows targets remain manual/WSL.

- [ ] **Step 7: Restack authentication PR #4523 only after #4221 is final**

Fetch the final PR #4221 head, then rebase or restack PR #4523 so it contains the new parent without duplicating installer implementation commits. Resolve conflicts by retaining #4221's installer service/API/UI and #4523's authentication service/terminal UI. Run the full backend suite, Harness Settings tests, frontend typecheck, and real Electron inspection again before pushing #4523.
