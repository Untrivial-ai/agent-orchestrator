# Issue 4850: lifecycle reliability implementation plan

Status: implementation and local verification complete, with native platform limits recorded below. Feature branch: `ao/agent-orchestrator-5/lifecycle-reliability-4850`.

Source: [issue #4850](https://github.com/Untrivial-ai/agent-orchestrator/issues/4850).
Analysis date: 2026-09-09. Checkout: `a47db0e067b9da107ee18c68e27ab1919d77ca3c`.
The seven core implementation files inspected during analysis matched their
revisions on `main` at `4748d55c3f737705be9d23dd77c6a1d1d0fbd69e`.

## Intended outcomes

1. Session termination succeeds only after the owned runtime and reviewer have
   stopped. An uncertain cleanup remains visible and retryable, with its
   workspace and ownership evidence preserved.
2. A timed-out spawn either rolls back completely or records an explicit,
   recoverable startup failure. It cannot silently leave an ordinary active
   session with missing handles.
3. A billing-blocked Actions job does not generate a request to repair tests.
   Repeating an equivalent check attempt does not generate another notification.

Implement the backend changes in the order below. Runtime teardown comes first
because both review cancellation and spawn rollback depend on its guarantees.
Keep the work in focused changes under this issue. Changes to the desktop or API
are limited to exposing a recovery or blocking reason when existing fields are
insufficient.

## Evidence and limits

The analysis used temporary Go source overlays, leaving the checkout unchanged.
Seven regression probes exposed these boundaries:

| Boundary | Observed result |
| --- | --- |
| Workspace creation exceeds the request deadline | Rollback leaves an active seed with empty runtime and workspace handles. |
| tmux process discovery fails | `Destroy` can still return success. |
| Processes survive the simulated forced signal | `Destroy` returns success without another exit probe. |
| A zero-step job reaches the active SCM observer | The observer reports CI as failing. |
| Active batch and pagination queries | Neither requests the execution evidence needed by the proposed classifier. |
| An unknown check accompanies a passing check | The existing per-check summary helper reports passing. |
| Only a check attempt URL changes | The notification signature changes. |

Selected existing tests passed after removing the inherited
`AO_TMUX_SOCKET_NAME` setting from the tmux test process. These are boundary
tests, not a reproduction of the original Raspberry Pi workload or proof that
the complete repository suite passes.

The reporter's original process tree, daemon logs, exact source revision, and
check/job payloads would identify which teardown path leaked and who initiated
the Actions reruns. Obtaining that evidence is useful for acceptance testing;
it does not block regression coverage for the confirmed code gaps. No automatic
Actions rerun path was found in the daemon during analysis.

## 1. Make runtime teardown verifiable

Primary files:

- [tmux runtime](../../backend/internal/adapters/runtime/tmux/tmux.go)
- [tmux unit tests](../../backend/internal/adapters/runtime/tmux/tmux_test.go)
- [tmux integration tests](../../backend/internal/adapters/runtime/tmux/tmux_integration_test.go)
- [runtime ports](../../backend/internal/ports/outbound.go)
- [session manager](../../backend/internal/session_manager/manager.go)

The existing order in `Manager.Kill` is broadly correct: destroy runtime and
reviewer, remove workspace, then mark termination. Strengthen the meaning of a
successful runtime destroy rather than moving the termination write earlier.

Implementation:

1. Change pane-process discovery and reaping helpers to return explicit errors.
   Distinguish confirmed absence from an unreadable process inventory, a
   signalling error, and an expired cleanup budget. Abort destructive teardown
   when ownership cannot be established.
2. Capture owned process identities before removing the tmux session. Retain
   pending teardown evidence in AO-owned durable storage until exit is verified,
   so retries after pane removal or daemon restart can find surviving children.
   Include process start identity and runtime generation where available;
   revalidate identity before signalling a persisted PID. Reuse existing runtime
   ownership contracts rather than introducing a general process manager.
3. Send TERM, allow a bounded grace period, send KILL to verified survivors, and
   wait for confirmed exit within a bounded cleanup context. Discovery and probe
   failures must propagate. Recognize exited/zombie processes as stopped rather
   than treating them as running workloads indefinitely.
4. Return success for confirmed completed cleanup, including repeated teardown.
   Keep pending ownership evidence, session recovery information, and the
   workspace when cleanup cannot be confirmed. A missing tmux session alone
   must not erase pending descendant cleanup.
5. Keep platform behavior explicit. Linux session matching and legacy macOS
   tmux cleanup need supported implementations; an unsupported matcher cannot
   mean success. Check native PTY/ConPTY callers for compatibility with the
   strengthened contract. Never target processes by command name or working
   directory alone.

Acceptance criteria:

- [x] Cooperative exit, TERM-resistant children, and repeated teardown work.
- [x] Discovery failure, signal failure, and inconclusive post-KILL probes return
      an actionable error without removing the workspace or reporting termination.
- [x] Retry works after the tmux pane disappears and after a daemon restart.
- [x] On Linux, PID reuse and an unrelated process with a similar command cannot
      cause an unrelated process to be signalled. macOS limits are recorded below.
- [x] An isolated real-process test confirms the owned child exits, including a
      background child. Exercise a descendant in a different process group and
      fail conservatively when ownership cannot be proven.

## 2. Finish active review cancellation before recording it

Primary files:

- [review engine](../../backend/internal/review/review.go)
- [review launcher](../../backend/internal/review/launcher.go)
- [review service](../../backend/internal/service/review/review.go)
- [reviewer contract](../../backend/internal/ports/reviewer.go)
- [review storage](../../backend/internal/storage/sqlite/store/review_store.go)

Decision: explicit cancellation of an active review will terminate its reviewer
runtime. The current adapter contract supplies an interrupt sequence, but no
reliable turn-cancel acknowledgement. Successful delivery of Escape or Ctrl-C
cannot prove the review stopped. Completed review history remains available;
an idle inspection pane with no running review retains its existing behavior.

Implementation:

1. Serialize cancellation with trigger and termination using the existing
   per-worker lock. Re-read the selected reviewer and running runs after
   acquiring that lock.
2. Attempt the adapter's graceful cancellation under a short budget, then use
   verified runtime teardown from phase 1. Failure to send the graceful interrupt
   must not prevent the teardown attempt. Keep this operation independent of
   caller cancellation once cancellation has been accepted.
3. After verified shutdown, cancel only the selected harness's running runs and
   clear its handle with `ClearReviewerHandleByHarness`. Preserve other reviewers
   and completed runs. The next trigger must create a new runtime.
4. Make the persistence step transactional where necessary, and retain retry
   evidence if recording cancellation fails after shutdown. A teardown failure
   must leave the run visibly unresolved and retryable. Ensure a concurrent or
   late result cannot overwrite a finalized cancellation.
5. Update cancellation help text and affected terminal tests for the intentional
   active-review behavior change. Apply the same verified-stop ordering to
   superseding an active reviewer where that path retires its runtime.

Acceptance criteria:

- [x] Cancellation returns success only after the active reviewer has stopped.
- [x] Interrupt delivery success followed by an unresponsive process still
      reaches verified teardown.
- [x] Teardown or persistence failure remains retryable without losing the
      selected reviewer identity.
- [x] Concurrent trigger/cancel, late submissions, and repeated cancellation do
      not create overlapping reviewers or overwrite final results.
- [x] Other harnesses, completed reviews, and idle inspection panes are preserved.

## 3. Make spawn rollback independent of request expiry

Primary files:

- [session manager](../../backend/internal/session_manager/manager.go)
- [Chat startup](../../backend/internal/session_manager/chat_spawn.go)
- [lifecycle manager](../../backend/internal/lifecycle/manager.go)
- [session storage](../../backend/internal/storage/sqlite/store/session_store.go)
- [reaper](../../backend/internal/observe/reaper/reaper.go)
- [session read model](../../backend/internal/service/session/status.go)

Implementation:

1. Start one bounded cleanup context using `context.WithoutCancel` at the failed
   startup boundary. Pass it through runtime, workspace, and database rollback;
   do not give each cleanup step a fresh full budget. Preserve request values for
   logging. Keep the startup operation itself cancellable.
2. Replace discarded rollback errors with explicit outcomes and joined errors.
   Delete an untouched seed after confirmed cleanup. Mark the session terminated
   only when runtime cleanup is confirmed. Preserve dirty workspaces and their
   recorded paths.
3. Handle `ports.RuntimeEffectError`, including `PossibleHandle`,
   `EffectOutcome`, and `CleanupOutcome`. A failed `Create` can have started a
   process. Destroy that exact possible runtime before workspace removal; never
   assume that an error means no resource was created.
4. Persist startup progress and cleanup failures as operation facts, including
   operation identity, stage, and known resources. Finalize startup alongside
   the committed launch. Derive a visible startup-failure or cleanup-pending
   result from these facts; do not persist a display status or reuse `no_signal`
   as a substitute for a known startup failure.
5. Extend existing reconciliation to recover abandoned startup operations and
   retry pending cleanup. Fence against a newer launch. A missing terminal
   handle alone is insufficient evidence: healthy Chat sessions intentionally
   have no such handle, and a new TUI spawn may still be in flight. Preserve a
   successfully committed spawn if only response delivery failed.

Keep new storage narrowly scoped to startup/cleanup facts. Use a new SQLite
migration if needed, update queries, and regenerate sqlc output. Define any new
wire fields in controller DTOs and the API registry before regenerating both
OpenAPI and frontend types.

Acceptance criteria:

- [x] Deadline expiry after seed insertion leaves no ordinary active seed when
      rollback succeeds.
- [x] Cancellation during provisioning, runtime creation, launch commit, and
      initial prompt delivery settles into a coherent outcome.
- [x] A runtime created before `Create` returns an error is tracked and cleaned
      up before its workspace is considered removable.
- [x] Database/cleanup failure records enough evidence for retry and restart
      recovery; dirty work is preserved.
- [x] Healthy Chat sessions, in-flight startup, and committed spawns whose HTTP
      response was lost are not mistaken for abandoned startups.

Promote the temporary timeout probe into permanent coverage, then add tests
using real SQLite storage and a cancelled HTTP request. Existing fake stores
and lifecycle implementations often ignore context cancellation, which is why
the ordinary rollback tests did not expose this defect.

## 4. Classify blocked CI and deduplicate equivalent attempts

Primary files:

- [GitHub projection helpers](../../backend/internal/adapters/scm/github/provider.go)
- [active GitHub observer](../../backend/internal/adapters/scm/github/observer_provider.go)
- [lifecycle reactions](../../backend/internal/lifecycle/reactions.go)
- [PR summary](../../backend/internal/service/session/pr_summary.go)

Existing work, as inspected during analysis:

| Draft | Use and remaining work |
| --- | --- |
| [#5088](https://github.com/Untrivial-ai/agent-orchestrator/pull/5088) | Reuse the classification work after covering the active batch query, pagination query, and observer summary. With this patch in a temporary overlay, a zero-step check became unknown but the active observer still reported failing. |
| [#5089](https://github.com/Untrivial-ai/agent-orchestrator/pull/5089) | Reuse the removal of per-attempt URLs from notification identity. The focused URL-only retry probe passes with this patch. Extend coverage to ordering and the active observation pipeline. |

Refresh draft status before implementation and avoid a competing replacement
for work that has already landed or is being continued.

Implementation:

1. Obtain execution evidence in the active observer. Update `scmPRFields`,
   `buildCheckContextsQuery`, and the older single-PR query consistently.
   Prefer an explicit billing refusal annotation. For ambiguous failed Actions
   jobs, preserve the reported failure until explicit blocking evidence exists.
   Zero steps alone do not establish a billing refusal. GitHub exposes [check annotations and steps](https://docs.github.com/en/graphql/reference/checks#checkrun)
   and [job runner/step metadata](https://docs.github.com/en/rest/actions/workflow-jobs#get-a-job-for-a-workflow-run).
2. Preserve the provider's raw conclusion and derive an actionable classification
   separately. Use the existing unknown CI/check states for confirmed blocked
   execution, with a bounded reason available to the user. Missing evidence,
   malformed workflows, and third-party check failures must not be silently
   reclassified as billing problems. The implemented query bounds annotations to
   five and requests step count and app identity in the same response. No
   additional REST lookup or cache is needed for this classifier.
3. Update `scmObservationFromGraphQL` so effective CI state comes from normalized
   evidence rather than blindly reusing the raw aggregate failure. Preserve
   complete-set/pagination guarantees. Genuine failed checks still fail; a
   passing check cannot turn a blocked or unknown sibling into a passing summary.
4. Normalize the set of failing checks before producing notification identity.
   Exclude attempt URLs/IDs, make ordering stable, and retain meaningful changes
   such as commit, check identity, or substantive failure content. Keep provider
   polling fingerprints separate: a new attempt still needs observation even
   when it should not generate a new notification.
5. Suppress worker repair notifications for blocked execution and preserve
   existing notification preferences. Show the blocking reason to the user.
   Do not add an Actions rerun mechanism; the original rerun initiator remains
   unconfirmed. A global notification cap is a separate policy decision.

Acceptance criteria:

- [x] A billing refusal stays blocked/unknown through fetch, persistence, session
      status derivation, and notification delivery.
- [x] The same classification works on the first batch and later check pages.
- [x] Real test failures, invalid workflows, absent metadata, third-party checks,
      and mixtures of failed/passing/unknown checks remain distinguishable.
- [x] Equivalent attempts and reordered check sets generate no duplicate repair
      notification, including across daemon restart.
- [x] A new commit or substantively different failure still generates a notice
      when the existing injection policy allows it.

## Verification and completion

Convert these acceptance criteria into runnable regression checks before
implementation. Promote useful temporary probes into permanent tests; temporary
files under `/tmp/ao-4850-analysis` are investigation artifacts, not the final
test suite. Each defect test must fail for the expected reason before its fix
and pass afterward.

Run narrow package tests while working on each phase, then the complete touched
package suites. Fix tmux test fixtures to control inherited runtime settings.
Real-process tests must use isolated runtime identities and clean up their own
children; they must never address the user's live sessions.

Before any implementation handoff, run the applicable workflow checks with their
declared environment. The backend baseline from `backend/` is:

```fish
go build ./...
go vet ./...
go test -race -timeout=15m ./...
```

Also check formatting and run the pinned linter through `npm run lint` from the
repository root. Match the API-drift workflow, including `npm run api`, when
validating the resulting backend change. If SQL changes, regenerate with
`npm run sqlc`. Generated artifacts must be committed with their sources when
implementation is eventually published.

Review the workflow path filters against the final diff and run every applicable
validation job before publication. Include CLI HTTP/error-envelope coverage and
the configured CLI smoke checks when those paths change. Run frontend checks
and renderer tests if recovery presentation or generated contracts change.

The current root `go.work` requires Go 1.26.5; `backend/go.mod` declares 1.25.7.
Use the repository's effective toolchain locally and record the actual CI
toolchain selected by each job rather than silently changing module versions.

Validate Linux process cleanup directly. Record native macOS and Windows gaps
until their relevant checks run; cross-compilation is not runtime validation.
Request a branch retest on the reporter's Linux arm64 host once a verified fix
is available. Keep optional evidence collection separate from code-level gates.

Completion requires all four phase acceptance sets, relevant complete suites,
and no unresolved ownership or rollback uncertainty hidden as success. Reconcile
the issue's three reported symptoms with the resulting changes before marking
the issue resolved. Do not close the entire issue for the CI-only drafts.

## Progress

- [x] Trace the issue and compare existing CI drafts.
- [x] Record implementation order, behavior decisions, and acceptance criteria.
- [x] Implement verified runtime teardown and active review cancellation.
- [x] Implement cancellation-safe spawn rollback and recovery.
- [x] Complete CI classification and deduplication.
- [x] Finish integrated verification and record platform limits.

Implementation decisions and recovery limits:

- Teardown captures process birth identities before removing tmux panes. Linux
  signals use pidfds to prevent PID reuse from redirecting a signal. The macOS
  implementation reads kernel birth identity and session ID, then revalidates
  before signalling. Native macOS execution is still unverified; numeric PID
  signalling there retains the platform race between the last check and signal.
- Pending ownership is stored beside the daemon run-file, scoped to its socket
  and handle. Creation and restart serialize with teardown and cannot reuse a
  handle whose cleanup is pending. Changed ownership is flushed before signalling;
  unchanged polls do not repeatedly flush the same record. Waiting for another
  runtime mutation respects cancellation and the teardown deadline.
- Cancellation closes an active reviewer runtime before atomically clearing
  its selected handle and cancelling running passes. Idle panes and completed
  history remain available. Failed initial reviewer delivery also performs
  verified cleanup and retains an unresolved handle when cleanup fails. Trigger,
  switch, and restore paths check the pending cleanup state under their worker
  lock before starting a reviewer. Termination deadlines include lock waiting.
- Startup ownership is journaled in migration 0130. Rollback uses one detached
  deadline, compares operation and controller generation before destructive
  work, and retains partial workspace resources. An absent in-memory Chat
  controller cannot establish provider exit. Startup-only changes emit the
  existing session change event through the database trigger.
- Recovery retries cleanup where resource ownership is known. Crashes inside
  resource creation can leave ownership unknown. These cases remain explicitly
  pending and require inspection. A committed launch with unrecorded prompt
  completion is preserved rather than stopped or replayed. Processes orphaned
  before these ownership records existed also require inspection on their host.
- Confirmed billing refusals appear as unknown checks with a bounded account
  reason in the existing PR display. Real failures retain their failure status.
  Attempt URLs, check ordering, and transport timestamps no longer create a
  duplicate repair notification.

Verification notes:

- Permanent tests cover exit confirmation, retry after restart, process reuse,
  interrupted spawn, partial runtime/workspace effects, generation changes,
  active cancellation, late results, billing persistence, and repeated notices.
- The real Linux fixture confirms termination of a TERM-resistant background
  child in a separate process group while an unrelated workload survives.
- Frontend: 286 files and 3,960 tests passed, with seven skipped. Typecheck and
  e2e typecheck passed. All 57 renderer smoke tests passed in the pinned
  Playwright 1.60.0 container with Node 24.20.0 and two workers. Its UID was 1000
  to match the workspace, compared with CI's 1001. No live desktop was started.
- Cloud client generation had no drift; typecheck, 21 tests, and packaging
  dry-run passed. Product UI typecheck, 124 tests, and packaging dry-run passed.
  The pinned browser runtime was prepared, and all 23 compatibility tests,
  including the native executable check, passed.
- OpenAPI and TypeScript generation was repeated with unchanged hashes. sqlc
  was regenerated after the startup migration and query changes.
- Go 1.26 uses GOTMPDIR for test fixtures as well as compiler scratch. Tests that
  validate project paths use /var/tmp/ao4850-tests, outside application state.
  Desktop environment overrides are removed for test processes.
- The initial broad race command exited nonzero. It caught the missing entry
  for migration 129 in the shipped-version ledger, which was fixed, along with
  environment and load-related test failures. Every failed package was rerun
  in full with the race detector after those corrections. The final SQLite
  suite passed in 519.579 seconds and its store suite in 287.766 seconds.
- All runtime adapter race suites passed after adding a regression that first
  confirmed cancellation could remain blocked behind teardown. The native Linux
  CLI end-to-end suite passed on the final sources in 8.396 seconds. The rebuilt
  clean-container install check passed and its container was removed.
- Final review and service/review race suites passed in 3.804 and 1.019 seconds.
  The added regressions first failed for six reviewer start paths and both
  cancelled stop paths, then passed with the cleanup and lock-wait guards.
- Final backend build and vet passed. The pinned linter reported zero issues;
  formatting and diff whitespace checks were clean. Backend race coverage comes
  from the broad run plus complete corrected reruns, rather than a single
  successful invocation of the original broad command.
- Linux arm64 and macOS arm64 runtime tests cross-compiled. Windows amd64
  runtime, review, Chat, and session-manager packages cross-built. These checks
  do not substitute for native macOS, Windows, or reporter-device tests.

All six local acceptance gates passed before publication. The machine-specific
test ledger and logs remain outside the published changes. The next validation
is the original workload on the reporter's Linux arm64 host, plus native macOS
and Windows checks.

The follow-up suggestions posted on the issue are separate from this change:
show recovery details in the inspector, bound background recovery scheduling,
and add an explicit resolution action for uncertain startup completion.

CI follow-up: fresh-host testing exposed an absent legacy tmux socket after
verified teardown. Socket selection now handles that absence while preserving
inconclusive liveness errors. Integration tests isolate both tmux namespaces.
After upstream added migration 0129 for change-log retention, the startup
migration moved to 0130 without modifying the upstream migration.

The upstream persistent Chat host can acknowledge shutdown before its provider
exits. Startup rollback now retains controller and workspace ownership after
that acknowledgement, including when projection failure previously detached
the controller. An exact-generation stop request remains fenced by the session
gate. These cases remain pending for inspection until host exit can be verified;
they cannot authorize workspace removal.

After integrating upstream, frontend typechecks and all 4,131 unit tests passed
(six skipped), as did all 59 renderer smoke tests. Cloud client checks and its
21 tests passed; product UI checks and its 126 tests passed. Local backend
verification encountered a full disk, so those failed attempts are retained in
the local logs and do not count as passing checks.

The complete merged backend race suites passed after moving temporary files to
task-scoped scratch space outside the full filesystem. Every non-SQLite package
passed in one invocation; SQLite, its helpers, and the store passed in a second
invocation (490.382 and 284.731 seconds for SQLite and store). Backend build,
vet, formatting, and golangci-lint 2.12.2 passed with zero findings. Native Linux
CLI end-to-end checks passed, including all 319 top-level tests, with one
macOS-only skip. API generation reproduced both artifacts without drift.

The merged fresh-install container and native macOS/Windows checks were not
repeated locally and require verification in the new remote jobs. The reporter's
Raspberry Pi workload and native macOS tmux descendant teardown remain separate
runtime validation gaps.
