# GLM Agent Discovery Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Resolve what “Zhipu GLM Agent” names and publish a reproducible accept/reject decision without registering an AO harness unless an official coding-agent terminal contract exists.

**Architecture:** This is a discovery-only plan. It produces a dated evidence record and executable, non-secret contract probes when an official artifact is found; it deliberately makes no domain, database, registry, API, UI, install, auth, reviewer, or Chat changes. If the intended product is ZCode or another differently named product, discovery ends with a new design request under that real identity.

**Tech Stack:** Vendor documentation, signed package/release metadata, shell-level CLI probing, ACP JSON-RPC fixture validation where officially supported.

**Spec:** `docs/superpowers/specs/2026-09-17-eight-agent-adapters-design.md`

## Global Constraints

- `glm-agent` remains absent from `backend/internal/domain/harness.go`, all SQLite constraints, both registries, API enums, and frontend choices throughout this plan.
- Community projects such as `glm-acp-agent`, `@xinghai123/glm-acp-agent`, and `zcode-acp-server` are leads only; they cannot establish an official Zhipu product contract.
- GLM Coding Plan model access through Claude Code, Cline, Kilo Code, OpenCode, or another existing harness is provider configuration, not a new AO agent identity.
- No reviewer changes and no production code changes are permitted by this plan.
- Official proof means a ZhipuAI/Z.ai-controlled documentation domain plus a vendor-controlled distribution identity or signed release.

---

### Task 1: Establish product identity and ownership

**Files:**
- Create: `docs/research/2026-09-17-glm-agent-product-contract.md`

**Interfaces:**
- Consumes: `https://docs.z.ai/llms.txt`, ZhipuAI/Z.ai official sites and organization repositories, signed registries, and the name supplied by the requester.
- Produces: an evidence table with exactly one disposition: `official-cli-found`, `different-product-found`, or `no-official-cli-found`.

- [ ] **Step 1: Record the search matrix before drawing conclusions**

```markdown
| Candidate | Vendor-owned docs | Vendor-owned artifact | Executable | Product type | Disposition |
|---|---|---|---|---|---|
| GLM Agent | URL or “none found” | package/release or “none found” | command or “none” | coding CLI/API/hosted agent | decision |
| GLM Coding Plan | URL | subscription/API | none unless proven | model provider | not an AO harness |
| ZCode | URL or “none found” | package/release or “none found” | command or “none” | separate product | separate design if official |
```

Search English and Chinese product names, but cite canonical vendor URLs and immutable package/release versions in the final record.

- [ ] **Step 2: Apply the ownership test**

For every artifact, record publisher/organization, package signature or release provenance, documentation owner, license, supported platforms, and whether the vendor describes it as the supported terminal client. Reject community wrappers as proof even when they speak ACP successfully.

- [ ] **Step 3: Verify the current negative evidence**

Confirm that `https://docs.z.ai/llms.txt` presents GLM Coding Plan as model access for existing coding tools and does not identify a coding CLI called GLM Agent. Distinguish hosted Slide/Poster/Translation Agent APIs from a local coding-agent process.

- [ ] **Step 4: Commit the discovery record**

```bash
git add docs/research/2026-09-17-glm-agent-product-contract.md
git commit -m "docs: identify glm agent product boundary"
```

### Task 2: Probe an official artifact only when Task 1 finds one

**Files:**
- Create: `docs/research/fixtures/glm-agent/version.txt`
- Create: `docs/research/fixtures/glm-agent/help.txt`
- Create: `docs/research/fixtures/glm-agent/session-events.jsonl`
- Modify: `docs/research/2026-09-17-glm-agent-product-contract.md`

**Interfaces:**
- Consumes: only the vendor-owned artifact accepted by Task 1.
- Produces: sanitized, reproducible evidence for each AO capability; creates no Go package or registration.

- [ ] **Step 1: Stop cleanly when no artifact exists**

If Task 1's disposition is `no-official-cli-found`, do not create the fixture directory. Add a dated conclusion stating that worker/orchestrator launch, prompt delivery, system instructions, model/permission configuration, session restore, activity, install/auth, and structured Chat are unsupported because no official process contract exists, then finish this plan.

- [ ] **Step 2: Capture version and help from an accepted artifact**

Run the vendor-documented version and help commands without credentials or a paid prompt. Redact usernames, paths, machine IDs, tokens, and account data before saving fixtures. Record the artifact version and integrity/signature next to the command transcript.

- [ ] **Step 3: Probe all terminal acceptance criteria**

```markdown
- persistent interactive worker and orchestrator process
- deterministic initial task delivery without a timed terminal write
- append-only system/instruction channel that preserves vendor safety prompt
- model or mode selection
- default, accept-edits, auto, and bypass permission mappings
- allow/deny tool confinement for restricted sessions
- provider-native session ID emitted without terminal scraping
- explicit-ID restore with model, permissions, environment, and instructions reapplied
- startup, active, idle/waiting, blocked, and exited structured events
- documented binary installation and non-secret auth readiness check
- macOS, Linux, and Windows support matrix
```

Mark each row `proven`, `unsupported`, or `not documented`, with command, output fixture, and vendor URL. One unsupported required row rejects production registration.

- [ ] **Step 4: Probe structured Chat separately**

If the official artifact documents ACP or another stable structured protocol, test initialize/authentication, new session, exact load, streaming text/reasoning/tools/plan, permissions, cancellation, process restart, replay, and capability negotiation. A successful handshake alone does not pass Chat.

- [ ] **Step 5: Commit sanitized evidence**

```bash
git add docs/research/2026-09-17-glm-agent-product-contract.md docs/research/fixtures/glm-agent
git commit -m "test: record official glm agent contract"
```

### Task 3: Publish the acceptance decision without registration

**Files:**
- Modify: `docs/research/2026-09-17-glm-agent-product-contract.md`
- Create: `docs/superpowers/specs/2026-09-17-zcode-agent-adapter-design.md` (only when the proven official product is named ZCode)

**Interfaces:**
- Consumes: Tasks 1–2 evidence.
- Produces: a final no-go decision or a separately named design; never a `glm-agent` registration.

- [ ] **Step 1: Write the decision table**

```markdown
| AO requirement | Evidence | Result |
|---|---|---|
| Official stable terminal product | vendor URL + pinned artifact | pass/fail |
| Worker/orchestrator | command transcript | pass/fail |
| Initial task | flag/protocol fixture | pass/fail |
| Standing instructions | append channel fixture | pass/fail |
| Model and permissions | help/protocol fixture | pass/fail |
| Native ID and exact restore | two-process fixture | pass/fail |
| Durable activity | event fixture | pass/fail |
| Install/auth readiness | vendor docs + probe | pass/fail |
| Structured Chat | protocol conformance | pass/fail/not offered |
```

- [ ] **Step 2: Enforce the naming decision**

If the artifact is officially named ZCode, create a fresh ZCode design that uses canonical harness ID `zcode`, cites the evidence, and repeats the repository's product requirements. Do not reuse `glm-agent`, because a model family and a product identity are not interchangeable.

- [ ] **Step 3: Verify no registration leaked into the repository**

Run:

```bash
rg -n 'HarnessGLM|"glm-agent"' backend frontend/src/api/schema.ts
git diff -- backend/internal/domain/harness.go backend/internal/storage/sqlite/migrations backend/internal/adapters/agent/registry backend/internal/adapters/chatdriver/registry backend/internal/httpd/controllers/dto.go frontend/src
```

Expected: the search has no production-code matches and the diff is empty for every listed production path.

- [ ] **Step 4: Validate documentation and commit**

Run: `npx @redwoodjs/agent-ci run --all`

Expected: all runnable documentation/workflow checks PASS; report unavailable network or vendor credentials as exact gaps.

```bash
git add docs/research/2026-09-17-glm-agent-product-contract.md docs/superpowers/specs/2026-09-17-zcode-agent-adapter-design.md
git commit -m "docs: close glm agent discovery gate"
```

When no ZCode design was created, omit that path from `git add`. The completion report must say explicitly that no AO harness, database identity, API enum, UI option, reviewer, or Chat driver was added.
