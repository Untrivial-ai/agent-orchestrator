# Isolated macOS differential v2

PR #4906 prepares this protocol but does not enable it. Current AO explicitly
sets macOS `disableDifferentialDownload = true` before checks and uses the stock
full-ZIP updater. `scripts/mac-differential-rollout.json` stays false and
`scripts/mac-differential-v2-trust.json` is an empty keyring. Windows/Linux flags
and feeds are unchanged. No bridge release, new server, release publication or
conductor mutation is part of this work.

## Permanent legacy boundary

The same GitHub release may eventually host both legacy ZIP updates and isolated
v2 assets. `latest-mac.yml`, `nightly-mac.yml` and `prN-mac.yml` must permanently
remain free of macOS `blockMapSize`, blockmap references and v2 metadata references.
No conventional `<ZIP URL>.blockmap` asset may exist, including aliases. Legacy
Provider derives precisely that suffix and has no v2 resolver. Its cached ZIP
and map cannot make it discover `.aoblockmap` or `ao-diff-v2-mac.json`.

A legacy client can cache a new conventional map even after missing-old-map full
fallback, then reconstruct on the next release. Neither withholding old maps nor
assuming adoption of an intermediate baseline protects those clients. The
protocol instead leaves their discovery paths permanently empty. The real
MacUpdater tests verify old clients take full ZIPs even when v2 assets exist on
the same release, and retain the conventional-sidecar activation counterexample.

## Metadata and identities

Exact metadata name: `ao-diff-v2-mac.json`, on the candidate's release. The
JSON envelope has exactly `payload` and `signature`. Unknown fields are rejected.

The signed payload has:

| Field | Contract |
| --- | --- |
| `schemaVersion` | Exactly numeric `2`; missing, string or unknown versions deny |
| `minimumClientVersion` | Canonical exact SemVer; running version must be greater than or equal, with SemVer prerelease ordering |
| `protocol` | Exactly `ao-mac-differential-v2` |
| `repository` | Exactly `Untrivial-ai/agent-orchestrator` |
| `channel` | Exactly `nightly` in PR1 |
| `enabled` | Exactly boolean `true`; false/malformed denies |
| `expiresAt` | Canonical UTC ISO timestamp, future and no more than 72 hours away |
| `candidate` | Exact version, `v<version>` tag and 40-character lowercase source commit |
| `artifacts` | One or two entries, unique `arm64`/`x64` architecture |

Each artifact contains `arch`, `zip`, `blockmap` and `baseline`. Every file
identity has exactly `url`, positive safe-integer `size`, and canonical base64
SHA-512. Baseline includes its version/tag/source commit plus ZIP and map
identities. Its version must precede the candidate and match the client's
current version; the actual cached ZIP must independently match size and SHA-512.
A baseline is reconstruction input, not a deployment/adoption requirement.

ZIP names must end with `-darwin-<arch>-<version>.zip`. Both target and baseline
maps use `<versioned ZIP filename>.aoblockmap`, for example:

```text
releases/download/v2.0.0/ao-diff-v2-mac.json
releases/download/v2.0.0/Agent.Orchestrator-darwin-arm64-2.0.0.zip
releases/download/v2.0.0/Agent.Orchestrator-darwin-arm64-2.0.0.zip.aoblockmap
releases/download/v2.0.0/Agent.Orchestrator-darwin-arm64-1.0.0.zip.aoblockmap
```

Both maps live on the candidate release; the signed baseline ZIP URL points to
its historical release. The candidate ZIP is the same full ZIP named by the
legacy feed. URLs must be canonical HTTPS GitHub release URLs for this exact
repository, without credentials, query strings, fragments or path escapes.
Aliases, duplicate architectures/assets, other repositories/releases and legacy
`.blockmap` paths are rejected. The resolver matches target URL/size/SHA-512
against electron-updater's offered full-ZIP identity before touching the output.

`signature` has exactly `keyId` and `value`. The value is base64 Ed25519 over the
UTF-8 canonical payload: recursively sorted object keys, array order preserved,
JSON primitive encoding and no whitespace. The compatible client separately
pins SPKI public keys by key ID; metadata cannot introduce its own trust key.
Unknown keys, invalid signatures, explicit denial, expiry and malformed values
all fall back. Authorization is re-read per attempt, not inferred from sidecar
presence. This signed manifest is the authoritative remote authorization. The
running implementation must also expose the compiled `mac-differential-v2`
capability. A qualifying version alone never grants capability, and neither
metadata nor settings can supply it. Local macOS, Nightly, Developer Mode and
rollout gates must still permit the attempt; architecture must match the running
client as well as the versioned artifact. A future conductor must refresh short-lived authorization deliberately;
expired historical metadata simply makes the update full-ZIP-only.

## Local generation and conductor contract

`scripts/mac-differential-v2.mjs` exports `generateMacV2Assets` and
`verifyMacV2Assets`. Node 24 supports the shared TypeScript schema import. No
package, feed, build, make or publish hook invokes generation. There is no upload
command. Explicit `allow: true`, Nightly channel, candidate/baseline identities,
minimum client version, local ZIP paths, expiration and an Ed25519 private key are required. Otherwise
no v2 assets are generated. The key is supplied by the caller and is never
serialized. Tests use ephemeral in-memory keys and temporary ZIPs.

Generation calls the existing pinned app-builder blockmap implementation with an
explicit `.aoblockmap` destination. It never creates a conventional sidecar, even
temporarily. The verifier checks the signature against an independently supplied expected candidate/channel context,
candidate ZIP bytes, both map
identities, exact v2 inventory, and forbidden legacy feed references/sidecars.
Unexpected v2 assets, aliases and injected conventional ZIP maps fail. Disabled
verification rejects every v2 asset. Existing Windows/Linux verification remains
independent and unchanged.

A separate reviewed `ao-releases` change must:

1. Preserve conventional macOS-map prohibition at generation, pre-upload and
   remote verification; do not weaken legacy feed policy.
2. Require explicit global authorization, channel/candidate eligibility and
   auditable v2 isolation/runtime evidence before calling generation. Missing or
   malformed authorization denies. No assumption about older-client adoption is
   permitted.
3. Sign the complete envelope using an independently managed trusted key, verify
   it and all referenced bytes before upload, and upload an explicit verified
   asset manifest rather than unrestricted `dist/*`.
4. Download and exact-inventory-check draft assets and run both legacy and v2
   verification again. Injected maps or metadata must fail this gate.

No conductor implementation or production key is included here. The remote kill
switch is a signed manifest with `enabled: false`, or removal of the specifically
named v2 manifest. Either makes subsequent attempts use one full ZIP. Keep the
independent client gate disabled and omit future maps during rollback. Preserve
historical ZIPs and maps; manifest removal is the explicit authorization
revocation operation, not release-history deletion. Short metadata expiry
bounds stale authorization; flags do not cancel a transfer already started.

## Supported extension and resource ownership

`MacDifferentialV2Updater` subclasses MacUpdater and overrides the protected
`differentialDownloadInstaller` method declared in electron-updater's shipped
TypeScript interface. Typechecking pins that declaration boundary. It never
calls the stock method or legacy map resolver, mutates private methods, edits
node_modules or patches a dependency prototype in product code.

The explicit resolver uses Electron's supported `net.fetch` API in production.
Metadata is bounded to 256 KiB, compressed maps to 8 MiB, decoded maps to 32 MiB,
and each merged range/copy operation to 1 MiB. Only one complete ZIP map with
contiguous coverage is supported; unknown map structures deny. Requests are
sequential, require exact HTTP status and Content-Range/length, and have a total
attempt timeout. Multi-range HTTP responses are not used.

Reconstruction uses exclusively owned FileHandles, never network pipes or
WriteStreams. Cached baseline bytes are SHA-512-verified on the same open handle
used for copies. Each response body is consumed/cancelled and settled before
moving on. Output is synced, read back and SHA-512-verified before success.
Both handle closes and required temporary-output removal are attempted even if
another cleanup operation fails. Cleanup errors are aggregated and terminal: the
subclass rethrows them, so no full fallback, cache promotion or handoff can begin
with unproven file ownership. Failed or cancelled output is removed before
MacUpdater receives an ordinary fallback or cancellation decision. There is no late callback holding a
raw descriptor when the full downloader opens its file.

On absence, ineligibility, signature/identity/map error, HTTP 416, timeout, reset,
bad range, reconstruction failure or hash mismatch, the method returns the
single fallback decision only after cleanup. MacUpdater then owns exactly one
full GET, its existing SHA-512 validation and native handoff. Cancellation is
terminal: it settles v2 work and starts no replacement transfer. A final signal
check after awaited reconstruction catches cancellation during digest or handle
cleanup before returning success to the dependency. A failed full
ZIP hash produces no handoff. The stock 6.8.9 differential worker remains
unsafe on 416; this implementation avoids it through the declared extension,
not by claiming that dependency was fixed.

Progress uses output completion for percent, target ZIP bytes for total, and
actual range-body bytes for transferred/speed. Metadata/map traffic and any
prior failed attempt are excluded. Logs expose only closed phase/error messages;
raw URLs, signatures, credentials and paths are not forwarded into telemetry.

## Verification and remaining acceptance

Run from `frontend`:

```sh
node node_modules/vitest/vitest.mjs run --config vite.main.config.ts scripts/mac-differential-v2.test.mjs scripts/mac-differential-v2-updater.test.mjs scripts/blockmap-reconstruction.test.mjs
npm run typecheck
```

The v2 harness invokes real MacUpdater `doDownloadUpdate`, `executeDownload`,
full HTTP download/digest/cache handling and the v2 subclass. Only transport
(Node loopback HTTP in place of Electron net), architecture probes and native
handoff are substituted. It verifies both architecture selections, exact ZIP
identity, legacy discovery isolation, denial/expiry/metadata/map failures,
range failures including empty/body/delayed/second-range 416, repeated failures
on one updater, no progress/ranges after fallback, sentinel descriptor survival,
cancellation and no handoff on a bad full digest. It also asserts stock
`differentialDownloadInstaller` is never invoked for v2.

These are standalone Node tests on macOS arm64 with simulated architecture
selection, not native x64 execution or packaged Electron acceptance. Real
Electron net, signed/notarized ZIP reconstruction, native Squirrel handoff and
installation still require packaged macOS verification before any enablement.
The independent upstream dependency proposal from helper 273 remains separate
supporting evidence with its own narrower coverage limits.
