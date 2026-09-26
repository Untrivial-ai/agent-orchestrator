# Reasonix Upstream System Prompt Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a process-scoped `--append-system-prompt-file` option to Reasonix so hosts can provide private system-role guidance without replacing user configuration.

**Architecture:** Parse the option at the interactive and `run` CLI boundaries, carry the path through existing rebuild options, and append validated UTF-8 content after Reasonix's configured prompt and project memory are composed. Keep the option ephemeral and make every rebuild/resume re-read the same file.

**Tech Stack:** Go, Cobra/pflag, Reasonix boot/controller tests

**Spec:** AO repository `docs/superpowers/specs/2026-09-26-reasonix-harness-design.md`

## Global Constraints

- Work in a separate checkout of `esengine/DeepSeek-Reasonix`, based on the then-current upstream `main`.
- The public flag is exactly `--append-system-prompt-file <path>` unless upstream maintainers request a rename with equivalent semantics.
- The file is process-scoped and must never update `reasonix.toml`, Reasonix home, project instruction files, or hook context.
- Preserve Reasonix's built-in/configured prompt, core policies, and hierarchical project instructions.
- Missing, unreadable, invalid-UTF-8, and empty files fail before a model turn.
- Prompt contents and the supplied path must not appear in diagnostics, JSONL events, or error strings.
- Do not claim AO compatibility until this change exists in a tagged release and the release asset passes live conformance.

## Review Focus

- A relative path after `--dir` resolves deterministically against the effective workspace, while AO's absolute path remains unchanged; Task 1 tests both.
- A token after `--` remains user prompt text even when it resembles the new flag; Task 1 pins parser termination.
- TUI model switches and `/reload` do not lose or duplicate the appended text; Task 2 exercises rebuilds.
- A full-trust extension replacing the system-prompt slot retains its existing contract; Task 2 verifies the append occurs before extension replacement rather than bypassing it.
- Errors never echo sensitive path or file contents; Tasks 1 and 2 assert sanitized failures.

---

### Task 1: Parse and validate the process-scoped file option

**Files:**
- Create: `internal/cli/append_system_prompt.go`
- Create: `internal/cli/append_system_prompt_test.go`
- Modify: `internal/cli/cli.go`
- Modify: `internal/cli/shell_completion.go`
- Modify: `internal/cli/shell_completion_test.go`

**Interfaces:**
- Produces: `resolveAppendSystemPromptFile(path string) (string, error)` returning a canonical absolute path without reading or persisting its content.
- Produces: `cliBuildOverrides.AppendSystemPromptFile string` and `boot.Options.AppendSystemPromptFile string` for Task 2.

- [ ] **Step 1: Write failing path-validation tests**

Add `TestResolveAppendSystemPromptFile` cases for an absolute readable UTF-8 file, a relative file, missing file, directory, empty file, and invalid UTF-8. Assert success returns an absolute path and every failure omits both the input path and file contents.

- [ ] **Step 2: Run the focused test and confirm failure**

Run: `go test ./internal/cli -run TestResolveAppendSystemPromptFile -count=1`

Expected: FAIL because `resolveAppendSystemPromptFile` does not exist.

- [ ] **Step 3: Implement the validator**

Implement `resolveAppendSystemPromptFile(path string) (string, error)` in `internal/cli/append_system_prompt.go`. Use direct filesystem APIs and UTF-8 validation; return fixed, non-sensitive error text. Do not copy the file or mutate configuration.

- [ ] **Step 4: Write failing CLI parsing tests**

Extend CLI tests to assert interactive and `run` accept the option, a missing value exits 2, invalid files exit before controller/model execution, and `-- --append-system-prompt-file` remains ordinary prompt text. Extend shell-completion tests to include the path-valued flag for root and `run` commands.

- [ ] **Step 5: Wire the flag into both command paths**

Register the path-valued flag in `runAgent` and `chatREPL`, resolve it after `--dir` handling, and store the canonical path in `cliBuildOverrides.AppendSystemPromptFile`. Map that field in `cliProfileBuildOptions` to `boot.Options.AppendSystemPromptFile`.

- [ ] **Step 6: Run the CLI suite**

Run: `go test ./internal/cli -count=1`

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/cli/append_system_prompt.go internal/cli/append_system_prompt_test.go internal/cli/cli.go internal/cli/shell_completion.go internal/cli/shell_completion_test.go
git commit -m "feat(cli): accept an external system prompt file"
```

### Task 2: Compose the prompt and preserve it across rebuilds

**Files:**
- Create: `internal/boot/append_system_prompt.go`
- Create: `internal/boot/append_system_prompt_test.go`
- Modify: `internal/boot/boot.go`

**Interfaces:**
- Consumes: `boot.Options.AppendSystemPromptFile string` from Task 1.
- Produces: `appendExternalSystemPrompt(base, path string) (string, error)`; the existing CLI rebuild closures retain `cliBuildOverrides.AppendSystemPromptFile`.

- [ ] **Step 1: Write failing composition tests**

Add table tests proving the helper appends exact UTF-8 content after configured prompt, core policies, and project `AGENTS.md`; uses exactly one blank-line boundary; leaves the base unchanged when no path is supplied; and returns sanitized errors for files changed to missing, empty, or invalid UTF-8 between validation and boot.

- [ ] **Step 2: Run the focused boot test and confirm failure**

Run: `go test ./internal/boot -run 'TestAppendExternalSystemPrompt|TestBuildAppendsExternalSystemPrompt' -count=1`

Expected: FAIL because the composition helper and option plumbing do not exist.

- [ ] **Step 3: Implement prompt composition**

Implement `appendExternalSystemPrompt(base, path string) (string, error)` in `internal/boot/append_system_prompt.go`. Invoke it in `build` immediately after `memory.Compose` and before extension snapshot assembly. The CLI's captured `cliBuildOverrides` must pass the path into each model switch and `/reload`, so every rebuild re-reads and appends it once.

- [ ] **Step 4: Add rebuild and resume regression tests**

Assert fresh interactive, fresh `run`, `--resume`, model switch, and `/reload` all contain one appended block. Assert a replacement extension still owns the final system-prompt slot according to the existing extension contract.

- [ ] **Step 5: Run focused and complete Reasonix tests**

Run:

```bash
go test ./internal/boot ./internal/cli -count=1
go test ./...
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/boot/append_system_prompt.go internal/boot/append_system_prompt_test.go internal/boot/boot.go
git commit -m "feat(agent): append process-scoped standing instructions"
```

### Task 3: Document and prove the public contract

**Files:**
- Modify: `README.md`
- Modify: `reasonix.example.toml`
- Modify: `site/src/pages/docs.astro`
- Modify: `internal/i18n/messages_en.go`
- Modify: `internal/i18n/messages_zh.go`
- Modify: `internal/i18n/messages_zh_tw.go`
- Test: `cmd/reasonix/main_test.go`

**Interfaces:**
- Consumes: the released CLI behavior from Tasks 1–2.
- Produces: a documented host-integration contract and a release version usable as AO's minimum version.

- [ ] **Step 1: Add a black-box CLI contract test**

Build the real `reasonix` command against a fake provider and assert the flag is accepted in TUI and `run`, composed as system-role content, absent from the user message, and absent from `--events-jsonl`. Assert missing/invalid files fail with sanitized output.

- [ ] **Step 2: Run the black-box test and confirm it passes**

Run: `go test ./cmd/reasonix -run TestAppendSystemPromptFileContract -count=1`

Expected: PASS.

- [ ] **Step 3: Update user-facing documentation**

Document the flag as a host/automation option, including composition order, process-only lifetime, resume behavior, absolute-path support, and failure/redaction rules. Update localized usage strings and shell-completion expectations.

- [ ] **Step 4: Run release-quality verification**

Run the repository's documented formatting, lint, test, and build commands. Confirm `reasonix --help` and `reasonix run --help` advertise the flag and no generated documentation is stale.

- [ ] **Step 5: Commit**

```bash
git add README.md reasonix.example.toml site/src/pages/docs.astro internal/i18n cmd/reasonix/main_test.go
git commit -m "docs(cli): document external standing instructions"
```

- [ ] **Step 6: Stop for upstream release**

Open or hand off the upstream Reasonix change for maintainer review. Do not begin AO production registration until a tagged release contains the contract. Record its tag, commit, asset checksums, supported platforms, and exact `reasonix --version` output in the AO conformance evidence.
