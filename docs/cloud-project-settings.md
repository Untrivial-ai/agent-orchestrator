# Cloud project settings

Cloud projects load from `GET /orgs/{orgId}/projects/{projectId}` and save through
`PATCH /orgs/{orgId}/projects/{projectId}/settings`. Local projects continue to use
the local daemon. A Cloud lookup or save failure stays a Cloud error.

Cloud and local settings render one `ProjectSettingsEditor` for General and
Agents. It owns the draft, inline fields, role rows, approval controls,
validation, and debounced autosave. Separate adapters load and save through
the control plane or daemon and provide each project's supported capabilities.
Local readiness, scratch/workspace fields, orchestrator replacement, Issue
Intake, session prefix, and Cues retain their existing local behavior. Pending
writes disable the shared editor, and the dialog waits for completion before
closing. Save errors retain the draft and allow retry. Model catalogs come
from the existing agent discovery API, without a local project lookup or a
Cloud project ID. OpenCode uses the connected Cloud credential type as its
catalog scope. Custom Cloud model IDs remain editable when the local catalog
does not list them, and effort choices are limited to the Cloud API values.
Cloud saves explicit model and effort selections even when the local catalog
marks them as defaults, since Cloud runtime defaults may differ. Cursor model
and mode selections are independent; changing either preserves the other.
Codex and Claude effort can be selected without a model override. The model
stays unset in project config, and Cloud applies the explicit effort at launch.
Project reads and writes stay on the control plane.

The settings endpoint accepts `displayName`, `defaultBranch`, and `config`.
Repository identity is read-only. `config.worker: null` or
`config.orchestrator: null` removes that role's defaults so new sessions use their
own agent selection. Other explicit `null` values and unknown fields are rejected.
Config uses the same role structure as local project settings:

```json
{
  "worker": {"agent": "codex", "agentConfig": {"model": "worker-model", "effort": "high", "permissions": "auto"}},
  "orchestrator": {"agent": "claude-code", "agentConfig": {"model": "orchestrator-model"}},
  "reviewers": [{"harness": "claude-code", "agentConfig": {"model": "review-model", "effort": "high", "permissions": "auto"}}],
  "autoReview": true
}
```

Cloud currently supports one project reviewer. Model, effort, mode, and
permissions are passed to the selected harness. Effort is supported for Codex
and Claude Code; Cursor supports plan and ask modes. OpenCode uses its AO prompt
agent and has no separate mode control. Empty strings select harness or session
defaults; an empty `reviewers` array restores the session-agent reviewer.

PATCH merges nested role objects under a project row lock. Omitted fields,
including unrelated config such as sandbox templates and agent rules, survive.
Reviewer arrays replace the previous array. Legacy `workerAgent` and
`orchestratorAgent` fields are normalized to nested roles on reads and project
writes. An existing nested `agent` wins over its flat alias. Flat aliases are
never emitted or stored by new project writes and are rejected by settings PATCH.
Creation retries compare canonical configs against historical command payloads,
so a legacy flat request or omitted config still returns the original project.
The stored command payload stays unchanged, and different choices still conflict.

Role defaults are stamped into newly created sessions. An explicit session
harness wins; matching role config supplies its model, mode, effort, and
permissions, and an explicit session model overrides the project model.
Existing sessions retain their saved launch settings.

Each review run stores its effective reviewer config before launch. It starts a
fresh process and conversation with that harness's credentials and an isolated
credential directory. Claude reviewers load only their isolated user settings,
leaving workspace hooks active for the worker alone. Later settings changes do
not alter that run. Older runs without a snapshot retain their session agent.
Missing reviewer config uses the session agent, model, and permissions; missing
`autoReview` enables reviews.

`autoReview` controls whether new AO review runs start. Session
`autoInjectReview` controls delivery of review feedback to the worker; disabling
injection does not disable reviews. Issue Intake and session prefix are not
exposed because the Cloud settings launch path does not consume them.
Local Cues settings stay in local project settings because Cloud does not consume them.
