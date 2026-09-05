# Desktop release architecture

Canonical desktop releases are conducted from the private
`Untrivial-ai/ao-releases` repository. That conductor is the only system allowed
to publish stable, nightly, or preview releases. The public
`Untrivial-ai/agent-orchestrator` repository supplies source and unsigned build
artifacts; it does not hold signing credentials or publish canonical releases.

Do not create release tags or try to publish from this repository. Operators
start and monitor releases in the private conductor, whose runbook contains the
authorized commands and recovery procedures.

## Release flow

At a high level, the conductor:

1. selects a public source commit and records its full SHA and release version;
2. dispatches `.github/workflows/build-artifacts.yml` in this repository with
   that pinned SHA and version;
3. downloads the resulting unsigned, short-lived workflow artifacts and
   `digests.json`, then verifies every artifact against the recorded digest;
4. performs signing, macOS notarization, packaging, and updater-feed generation
   in the private release repository;
5. uploads the complete release as a draft;
6. runs remote verification against the draft artifacts and feeds;
7. publishes the verified release atomically; and
8. updates Homebrew where the channel requires it.

Publication does not proceed when a digest, signature, notarization result,
feed, artifact set, or remote verification check differs from what the
conductor expects. A failed run is resumed or replaced from the conductor; it
is never bypassed by publishing directly from this repository.

## Public unsigned build boundary

`.github/workflows/build-artifacts.yml` remains intentionally dispatchable. It
accepts an explicit public ref/SHA and version, builds the four supported
desktop targets, and uploads unsigned workflow artifacts plus their SHA-256
digests. It has read-only repository permissions, does not use release signing
secrets, and does not create, edit, or publish GitHub Releases.

The private conductor pins the immutable source SHA before dispatch and treats
the returned digests as the handoff boundary. Signing credentials and release
publication permissions stay in `Untrivial-ai/ao-releases`.

Windows installers follow the same boundary (#4502): the NSIS maker
(`frontend/makers/maker-nsis.ts`) activates electron-builder code signing only
when signing credentials are present in the environment, so the public build
stays unsigned while the conductor signs with its own credentials downstream.

## macOS signing contract for the bundled ACP runtime

This repository builds unsigned, so `packagerConfig.osxSign` — and the per-file
hook `macSignOptionsForFile` in `frontend/forge.config.ts` — never runs on
published bytes. The canonical signer re-signs the bundle downstream and is the
only place the following contract is enforced. Whoever signs a bundle containing
`<App>.app/Contents/Resources/acp-runtime/node/bin/node` must:

- give the nested Node `com.apple.security.cs.allow-jit` **and**
  `com.apple.security.cs.allow-unsigned-executable-memory` if and only if the
  binary contains an x86_64 Mach-O slice (a thin x64 binary, or a universal
  carrying an x64 slice); an arm64-only binary keeps `allow-jit` alone. V8 uses
  mprotect for JIT on x64, which Hardened Runtime blocks without the second
  entitlement, and the Node then crashes in `SetPermissions` (#3879);
- derive that decision from the Mach-O header of the file being signed
  (`frontend/makers/macho-archs.ts`, `machoHasX86_64Slice`), never from the
  signing host's architecture (`process.arch`, `uname -m`): one signing host
  handles both architectures;
- fail the signing pass when that binary cannot be read or parsed — never fall
  back to the host architecture or to no entitlements;
- leave every other bundle file on `@electron/osx-sign` defaults; widening the
  executable-memory entitlement beyond the x64 ACP Node is out of scope.

Before publication, the final artifacts must prove the contract: the nested
Node's entitlements are inspected in the signed bundle, and the shipped x64
zip's Node actually executes JavaScript on a native Intel runner (the public
build matrix's `macos-15-intel` leg is the precedent). Keep the arm64
verification as well.

The verifier-side counterpart is rule 5 of
`frontend/scripts/verify-mac-artifact.sh`: `codesign -d --entitlements :-` on
the nested Node, `lipo -archs` for the arch decision, and a guarded `node -e`
execution only after codesign, Gatekeeper, and stapler all pass. The JS helper
and `lipo` agree on the one invariant that matters — "contains an x86_64
slice"; `lipo` may print `arm64e` for an arm64 slice (it names slices from
cpusubtype) where the helper reports `arm64` (it classifies by cputype), which
is intentional. Confirm what actually landed on the bytes with
`codesign -d --entitlements :- <node> | plutil -p`.

## Channels

- **Stable** releases are deliberate production cuts. After all verification
  gates pass, publication updates the stable updater feeds and the Homebrew
  distribution metadata.
- **Nightly** releases are conductor-scheduled builds from the configured public
  source line. They remain prereleases and update only nightly feeds.
- **Preview** releases are conductor-requested builds for an isolated candidate
  or change. They remain prereleases on their own preview channel and cannot
  replace stable or nightly feeds.

Version selection, channel naming, retention, and promotion policy belong to
the conductor. `frontend/package.json` may be stamped during an unsigned build,
but changing it in this repository is not a release trigger.

## Artifact verification

The conductor owns the authoritative verification and publication gates; this
script is the local diagnostic counterpart, not the publication gate. For a
local diagnosis of a downloaded macOS artifact, use the repository's supported
verification script:

```bash
frontend/scripts/verify-mac-artifact.sh <artifact>
```

The script preserves the archive's code-signing metadata while extracting and
runs the expected signature, Gatekeeper, and notarization checks. Do not use a
plain `unzip` result to judge a macOS signature.

Stable macOS releases continue to include both a DMG for first installation and
a ZIP for electron-updater. The ZIP and `latest-mac.yml` must remain available:
electron-updater cannot install an update from a DMG. Nightly and preview
channels omit the first-install DMG.

## Incident rule

Exactly one publisher is a correctness requirement. If the conductor is
unavailable or a release is partially staged, stop and recover through the
private runbook. Do not dispatch a public publishing workflow, create a
substitute release, mutate an existing release, or publish from a second
identity.
