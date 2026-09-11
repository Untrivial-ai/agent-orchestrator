# Isolated macOS differential v2 implementation

Existing PR: #4906. Rollout stays disabled and draft. No publication or conductor mutation.

1. Define a signed v2 envelope hosted on the candidate's existing GitHub release. Bind exact schemaVersion 2, enabled authorization, minimumClientVersion, channel, candidate version/tag/source commit, architecture, target ZIP URL/size/SHA-512, baseline identity and baseline ZIP/map hashes. Permit only distinct versioned `.aoblockmap` assets; never conventional `.zip.blockmap` or aliases. Pin verification keys independently in the compatible client.
2. Add an opt-in local asset generator and verifier for the future conductor contract. Legacy feeds retain only full ZIP metadata and never reference v2. Verify exact v2 inventory and signatures before future publication; document independent conductor work.
3. Require the locally compiled mac-differential-v2 capability independently of version and signed remote authorization. Implement an explicit v2 resolver with bounded authenticated metadata/map reads, exact candidate/baseline validation and no discovery through legacy blockmap URLs.
4. Use electron-updater's declared protected `differentialDownloadInstaller` extension through a MacUpdater subclass. Implement v2 reconstruction with sequential bounded range reads and exclusively owned file handles, closing and settling all work before returning the single full-download fallback decision. Do not patch dependencies or invoke stock differential code. Preserve native full-download digest/handoff behavior.
5. Exercise real MacUpdater through the subclass: both architectures, metadata/signature/identity errors, missing assets, cached baseline mismatch, HTTP 416/body/reset/incorrect range, reconstructed/full digest failures, revocation/cancellation, exact one full fallback and one verified handoff, descriptor integrity and no work after fallback. Preserve legacy absence/cache-cycle counterexamples.
6. Re-run focused tests, typechecks and CI; record honest packaged Electron/net/Squirrel limitations. Push only the existing PR branch.

The remote schema and compatibility criterion are approved and implemented.
`ao-diff-v2-mac.json` lives on the same release as the normal ZIP. Eligibility
requires the compiled `mac-differential-v2` capability plus schemaVersion exactly
2, enabled exactly true, minimumClientVersion, channel, architecture and matching
baseline/candidate/ZIP/map identities, URLs, sizes and SHA-512 values. Version
alone is insufficient. Missing, removed, disabled, malformed or mismatched
metadata denies and selects one full ZIP. Old clients never request this asset.

Current open acceptance: trusted production signing keys and conductor verification are not configured; packaged macOS acceptance is not established. No bridge release or minimum-installed-baseline adoption assumption is permitted. The conventional sidecar prohibition remains permanent for legacy-reachable URLs.

Implementation status: steps 1 through 5 are implemented and locally verified.
Step 6: 271 focused tests pass on actual Node 24.20.0; project and E2E typechecks
and unsigned packaging pass. CI for implementation commit 4dde125f5 passed
3861 tests (6 skipped), renderer smoke, both typechecks and secret scanning.
The independent cancellation review finding was fixed and its thread resolved.
Packaged v2 Electron net/native acceptance remains outstanding. Production keys
and deployment of the approved manifest contract in the separate conductor are
not configured. These are execution and acceptance work, not pending schema or
compatibility decisions. The feature stays disabled.
