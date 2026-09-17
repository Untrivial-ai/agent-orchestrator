# Desktop project onboarding contract review

Scope: local desktop project creation, cloning, existing-folder imports,
workspace imports, Git preparation, registration, and first orchestrator startup.
This is a code-path review prompted by the `untrivial` empty-clone failure, not
an exhaustive interactive audit. Cloud provisioning, mobile onboarding, and
all native picker/platform combinations remain outside this review.

## Root cause across stages

Onboarding currently has several different definitions of readiness:

- `service/importer.Validate` checks repository metadata, commit/origin presence,
  and required preparation actions.
- `service/project.Add` validates and registers a repository with a commit.
- The workspace adapter later requires a resolvable base ref to create a session.
- The selected agent/runtime has separate launch prerequisites.

These distinctions are legitimate, but the flow does not expose them as an
explicit contract. Completing Git preparation does not establish that the next
workspace can be created. The reported failure crossed precisely that boundary.

## Changes in PR #5126

- Clone destinations can be typed and default to `~/ao/projects` without a
  saved selection. A separate button opens the native picker at that folder,
  creating it if needed; the picker permits folder creation. Existing user
  selections remain respected, and cloning creates missing destination parents.
- Default resolution accepts an AO-initialized local branch before the first
  push, without guessing from an arbitrary current checkout. Remote defaults
  retain precedence, and a failed fetch of a known default still fails.
- Both the project initializer and the import preparation commit action use
  `gitdefault.Resolver.RecordInitialBranch`. Empty clones already have `.git`
  and skip `git init`, so recording the branch only in the init action was
  insufficient. Branch identity is recorded before the initial commit to retain
  it across a failed commit/retry.
- Legacy detection remains compatibility support for already-created projects;
  new onboarding does not depend on a particular commit message or `main`.
- Tests cover `main` and `trunk`, custom initial-commit messages, the real
  clone → Git preparation → registration → first-worktree sequence, and the
  transition to an advertised remote default after pushing. They do not launch
  an external agent or claim to cover the whole native desktop interaction.

## Further findings and design direction

### One branch-selection policy across entry points

`CreateProjectFlow.createProject` reads the checked-out branch for local imports
and submits it as an explicit default override. Its clone path does not do so
and relies on automatic resolution. The same checkout can therefore acquire
incompatible base-selection policies depending on how it was added.

Define the product choice explicitly: automatic repository default versus a
user-selected base branch. The daemon should resolve automatic defaults using
the same policy for every client. Merely opening a local repository on a feature
branch should not silently count as choosing that feature as its default.
This policy discrepancy is not changed by #5126.

### Distinguish registration from launch readiness

Keep registering a project separate from launching an agent: an unavailable
agent or an offline remote should not erase a successfully registered project.
Expose typed, actionable readiness facts for workspace creation and agent launch,
and check the same prerequisites again at launch because external state can
change. The UI should show “project added; startup needs attention” with a
specific repair/retry action, rather than treating every preceding green step
as evidence that startup must succeed.

Reuse existing Git resolution and agent readiness services for these facts;
do not introduce another independent validator in the renderer.

### Consolidate orchestrator startup and errors

Automatic startup in `routes/_shell.tsx` calls `/sessions` directly and converts
failures to strings. Later startup uses `spawnOrchestrator`, `/orchestrators`,
and a typed error. The formatter then interprets codes and repository details
embedded in text. Review the endpoint defaults and consolidate the shared
startup/error contract while preserving the chosen harness and session mode.
This is an architectural follow-up, not part of #5126.

### Make preparation recovery explicit

Prepared clones already have server-side ownership markers and registration
checks, which protect cleanup from removing registered projects. However,
`usePreparedClone` holds the client's preparation identity only in memory.
There is no resume flow in this hook after a renderer restart. Define how an
interrupted preparation is discovered and resumed or deliberately abandoned;
do not solve this by blindly deleting leftover checkouts.

Existing-folder preparation and remote repository creation also have external
side effects. Preserve completed work on retry and clearly identify what was
created, what failed, and what the next attempt will repeat.

### Validate journeys, not just components

Retain focused tests, but add boundary/acceptance coverage for these journeys:

| Journey | Required outcome |
| --- | --- |
| Populated remote clone, including non-main defaults | First workspace uses the repository default |
| Empty remote clone | AO's initial branch seeds the first workspace; first push supersedes local fallback |
| Existing local repository on a feature branch | Explicit, consistent base-selection policy |
| Plain folder or unborn local repository | Approved initialization preserves contents and records branch identity |
| Workspace with mixed child readiness | Each child has an actionable result; failed preparation is retryable |
| Missing Git, agent, auth, or offline remote | Failure identifies the stage and preserves completed project setup |
| Cancel, restart, or lost response during preparation | No accidental deletion, duplicate registration, or invisible abandoned operation |
| Project registration succeeds, startup fails | Project remains available and startup alone can be retried |
| Repeated submission or duplicate/aliased path | One registered project with deterministic recovery |

Some of these have individual tests already. The remaining work is to exercise
stage-to-stage guarantees through shared services and desktop acceptance tests,
not to infer journey coverage from the total count of unit tests.
