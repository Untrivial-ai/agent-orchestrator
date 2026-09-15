# Claude Auth and Provider Correctness Design

## Goal

Close the current PR review gaps in the local AO application so Claude model and effort selection, credential validation, live authentication recovery, and session restore remain correct across first-party Anthropic, gateways, Bedrock, Vertex, and Foundry.

## Scope and constraints

- Keep the change local to `backend/`, `frontend/`, and `packages/mobile/`; the PR must contain no `cloud/` changes.
- Preserve the invariant that AO reports `unauthorized` only from positive evidence of credential rejection. Permission failures, ambiguous gateway responses, transport failures, and unsupported discovery calls remain `unknown`.
- Provider model IDs and effort levels remain provider-owned data. Static Claude aliases are a last-resort display fallback, never a successful provider-discovery result that may overwrite a last-known-good provider catalog.
- Tests use local fakes and `httptest`; they make no external provider calls.
- Existing unrelated working-tree edits remain untouched and unstaged.

## Runtime authentication recovery

The generic ACP layer will recognize only protocol-defined authentication errors. It will no longer classify arbitrary error text with a provider-agnostic phrase list or treat a generic HTTP 403 code as proof of invalid credentials.

Claude's ACP binding will own recognition of Claude's synthetic terminal failure contract. A provider-specific callback will inspect the exact structured terminal outcome attached to a nominally successful `stopReason=end_turn` response. Only the structured login action marks the turn as `ErrChatAuthRequired`; ordinary assistant prose mentioning authentication remains content, not an auth signal.

The Claude driver will receive a daemon-owned rejection callback. On a live rejection it will invalidate the private Claude credential cache, invalidate the Agent Service readiness snapshot for Claude, and schedule a non-blocking readiness refresh. The callback must not make the turn wait on provider I/O.

The desktop composer will not disable submission solely from a cached unauthorized status. Submission remains available so the launch-time readiness endpoint can revalidate credentials after an external login; only that fresh launch check blocks an unauthorized launch.

## Provider validation contracts

- Anthropic OAuth/setup-token requests retain the Claude Code headers: bearer authorization, the required `anthropic-beta` values, `x-app: cli`, and a Claude Code user agent.
- Anthropic first-party rate limiting may count as authenticated. A custom `ANTHROPIC_BASE_URL` gateway returning 429 remains unknown without authenticated evidence.
- Generic 403 responses remain unknown. A provider may classify 403 as invalid only when its documented response body positively identifies credential rejection.
- Bedrock `ListFoundationModels` supplies catalog data only. It does not establish launch readiness because list and invoke permissions are independent.
- Vertex discovery uses the documented Model Garden publisher listing shape, `GET /v1beta1/publishers/anthropic/models`, and preserves project/location context only where the API contract supports it.
- Anthropic model discovery requests the maximum supported page size and follows `has_more`/`last_id` until complete, with repeated-cursor protection.

Each contract will be pinned by an `httptest` regression that asserts the request URL, headers, pagination behavior, and verdict classification.

## Project-scoped credential discovery

Command execution for AWS and gcloud credential-chain fallbacks will accept the discovery request's merged launch environment and working directory. The command seam will return stdout and stderr separately. Successful token parsing reads stdout only; stderr is retained solely for error diagnostics.

`claude auth status` will run with the same project working directory and merged environment as launch. Its reported provider may refine discovery only in that same context and cannot override explicit project provider configuration with daemon-global state.

The model-catalog fingerprint will no longer claim completeness from a subset of mutable credential inputs. Claude provider catalogs will be revalidated through discovery before being considered current; on discovery failure, the service retains a usable last-known-good provider catalog. Static aliases are returned only when no usable provider catalog exists.

## Model and effort lifecycle

Claude TUI launch validation will occur before durable session state is created, using the same discovered model catalog and per-model effort rules as Chat. Unsupported or stale model/effort pairs fail before spawn side effects.

The resolved model and effort will travel together through `ports.RestoreConfig`, `agentruntime.RestoreConfig`, Claude's restore adapter, and `buildClaudeRestore`. Restored Claude sessions therefore emit the same model and effort flags as their original configured session.

Mobile will map `configured` credentials to neutral/unknown readiness. Only verified `authorized` state renders and ranks an agent as ready.

## Error and cache behavior

Provider discovery returns its real error to `Service.Models`. The service uses that error to select the stale-cache path instead of saving static aliases as a successful result. If no prior provider catalog exists, the response may show static aliases while retaining an unverified/unknown provenance.

Live auth rejection invalidation is idempotent. Recheck scheduling is best-effort and logged; inability to refresh immediately must not hide the original turn failure or deadlock the conversation.

## Verification

Focused regressions will cover:

1. Claude assistant auth-expiry terminal metadata plus nominal ACP success.
2. Arbitrary assistant auth prose remaining non-fatal.
3. Both auth caches invalidated and a readiness refresh scheduled.
4. Composer submission after an externally repaired login.
5. Discovery errors preserving last-known-good provider IDs and efforts.
6. Vertex URL shape, OAuth headers, 403 semantics, Bedrock catalog-only semantics, gcloud stdout parsing, project environment propagation, gateway 429 handling, and Anthropic pagination.
7. Claude TUI pre-spawn model/effort rejection and restore argument preservation.
8. Mobile `configured` state mapping to auth-unknown.

Run the narrow package/frontend tests for each change first. Before pushing, run the exact pinned lint command and relevant type checks; broad platform and integration coverage runs on PR CI.
