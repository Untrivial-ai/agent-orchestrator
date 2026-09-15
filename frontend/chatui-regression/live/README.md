# ChatUI live certification catalog

[`scenarios.json`](scenarios.json) is the stable, machine-readable catalog for the
12 ChatUI issues found in the 2026-08-25 deep QA pass. It defines dispatchable
preconditions, ordered actions, expected results, lane ownership, and required
evidence. It records the user-supplied todo repository as the local default, but
contains no credentials or generated runtime paths.

Schema version 1 uses `providersByLane` on every scenario. The map must contain
the primary `lane` and every `supplementalLanes` entry. Each array names the
providers required for that lane; `scripted-chat-driver` is deterministic-only and
must never be selected by a packaged-Electron executor.

The operating procedure, capture/strict semantics, isolation requirements, and
safe packaged-Electron cleanup are documented in
[`docs/testing/chatui-regression-harness.md`](../../../docs/testing/chatui-regression-harness.md).

A consumer must reject unknown schema versions, operations, or assertions. It must
also report missing live prerequisites as `blocked`; silently skipping a scenario
or treating a capture-mode exit code as success is not certification.
