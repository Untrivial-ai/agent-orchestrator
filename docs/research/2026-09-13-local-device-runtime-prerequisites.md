# Local virtual-device runtime prerequisite audit

Date: 2026-09-13
Status: Evidence for the proposed device-runtime architecture; no runtime is
shipped by this document.

## Question

Can AO use existing open-source components to add local iOS Simulator and
Android Emulator control on macOS without violating AO's daemon, security,
state, packaging, or cross-platform boundaries?

## Conclusion

Yes, conditionally. `expo-device-hub` and `agent-device` are strong building
blocks, but the current artifacts cannot be copied into AO unchanged.

- The architecture fits AO if the Go daemon owns the public API, authorization,
  process lifecycle, route allowlists, and error normalization.
- The audited licenses permit redistribution, subject to retaining license and
  notice material and meeting the `node-datachannel` MPL-2.0 obligations.
- AO's pinned Node 22.23.2 satisfies the helpers' declared requirements.
- The exact Expo iOS binaries are arm64-only and require macOS 14 or newer.
  The initial verified feature target must therefore be Apple Silicon macOS
  14+, unless another iOS capture transport is implemented and tested.
- Both helpers can start without SDKs present and report missing-tool facts, but
  their raw diagnostics are not safe public errors.
- The helper npm graph has an install script and native binaries. AO must build
  and verify a locked, per-architecture runtime during packaging; it must not
  run npm or download helpers on first use.
- Live iOS/Android device acceptance remains a feature-PR gate because the
  audit machine had neither full Xcode nor the Android SDK/emulator toolchain.

This prerequisite PR deliberately adds no device API, CLI, skill page, UI, or
packaged dependency. Those surfaces must land atomically in the first
user-visible device PR.

## Sources and reproducibility

The audit used immutable versions and commits:

| Component | Version / revision | Source |
| --- | --- | --- |
| Existing AO Android experiment | PR #3882, open at audit time | [Untrivial-ai/agent-orchestrator#3882](https://github.com/Untrivial-ai/agent-orchestrator/pull/3882) |
| T3 Code local-device reference | PR #10677, merged 2026-09-10 | [pingdotgg/t3code#10677](https://github.com/pingdotgg/t3code/pull/10677) |
| T3 helper selection | `expo-device-hub@0.9.0`, `agent-device@0.20.10` | [DeviceToolchain.ts at the reviewed T3 commit](https://github.com/pingdotgg/t3code/blob/3486451525ecaf3a8379aa6d08c885c7cc5335e3/apps/server/src/device/DeviceToolchain.ts) |
| Expo Device Hub | npm `0.9.0`, git `87c2037f517bbbae76a1bb7bdca8324fd6d4272e` | [Expo source at the npm git head](https://github.com/expo/expo-device-hub/tree/87c2037f517bbbae76a1bb7bdca8324fd6d4272e) |
| Vendored Expo serve-sim | git `55fec25295316e9757bf376c136275b59aea3df5` | [Expo serve-sim source](https://github.com/expo/serve-sim/tree/55fec25295316e9757bf376c136275b59aea3df5) |
| Agent Device | npm `0.20.10`, git `fda81c5121f0a4307966040c763bfb5edf0ccdf3` | [Callstack source at the npm git head](https://github.com/callstack/agent-device/tree/fda81c5121f0a4307966040c763bfb5edf0ccdf3) |
| Apple platform prerequisites | Current Apple documentation | [Downloading and installing additional Xcode components](https://developer.apple.com/documentation/xcode/downloading-and-installing-additional-xcode-components) |
| Android platform prerequisites | Current Android documentation | [SDK command-line tools](https://developer.android.com/tools) and [emulator acceleration](https://developer.android.com/studio/run/emulator-acceleration) |

Registry metadata and tarballs were queried with:

```text
npm view <package>@<version> name version license repository engines dependencies dist gitHead --json
npm pack expo-device-hub@0.9.0 agent-device@0.20.10 --json
```

The tarballs were unpacked under a temporary directory. Their manifests,
licenses, executable formats, load commands, and minimum OS versions were
inspected with `jq`, `file`, `otool`, `vtool`, and `codesign`. A temporary npm
install supplied the resolved dependency graph and smoke-test runtime.

## Artifact identity

| Package | npm integrity | SHA-1 shasum | Packed / unpacked | Declared license |
| --- | --- | --- | --- | --- |
| `expo-device-hub@0.9.0` | `sha512-zDiSNQIKldjat1emL5X5/XCMMtVbXGtQYkQYlnK77CxtEaRBQKqKbBxjKF3+qtpMAPKxGZYOvc9uk4HmYdO3sg==` | `8d7c32ed21ab5b8c4c6118f6c86a5b8d71c6cba0` | 9,317,432 / 19,540,126 bytes | MIT |
| `agent-device@0.20.10` | `sha512-baj3tQQ1JH/FbJ3x3FVmSn4nctOlFmdYsiz4LNZh3rEOj/8g3hkIq07lcUii07lMSQ7hCiY1m8+SyWPAX1ykow==` | `67715507e377c00abe3173e319407472d3f8a01b` | 913,655 / 3,121,970 bytes | MIT |

Both registry records carried npm signatures. Expo's record also carried an
npm SLSA provenance attestation. These facts help establish origin; they do not
replace AO's checked-in lockfile, per-file integrity manifest, or source review.

Installing both packages together resolved 68 npm packages and occupied about
49 MB unpacked on the audit host. The exact total will change if caret-ranged
transitives are resolved again, which is why a reproducible AO build must use a
committed lockfile and `npm ci` rather than two direct version pins alone.

## License inventory

The resolved temporary lock contained:

| SPDX declaration | Package count |
| --- | ---: |
| MIT | 46 |
| Apache-2.0 | 11 |
| ISC | 7 |
| MPL-2.0 | 1 |
| BSD-3-Clause | 1 |
| `(BSD-2-Clause OR MIT OR Apache-2.0)` | 1 |
| `(MIT OR WTFPL)` | 1 |

No GPL, AGPL, SSPL, or source-available-only declaration appeared in the
resolved package manifests.

Material findings:

- `expo-device-hub` is MIT and ships its license in the tarball.
- Its vendored `serve-sim` and `serve-emu` copies are Apache-2.0 and ship their
  license files. `serve-sim` also ships its attribution `NOTICE`.
- The bundled LiveKit WebRTC framework contains a WebRTC BSD-style license in
  `Resources/LICENSE.webrtc`; binary redistribution requires keeping it.
- `agent-device` is MIT and ships its license.
- `node-datachannel@0.32.3`, used for Android WebRTC, declares MPL-2.0. AO must
  preserve its license and make the corresponding covered source available as
  required when distributing the executable form. If the package is pruned to
  binaries, the release process must retain a source/notice path rather than
  silently dropping it.
- `agent-device` also ships Android helper APKs and Apple runner source. They
  are part of the reviewed distribution and must be covered by the packaged
  third-party notice and integrity manifest.

This is an engineering inventory, not legal advice. The release PR should have
the final third-party notice and MPL handling reviewed before publication.

## Runtime and architecture compatibility

`agent-device@0.20.10` declares Node `>=22.12`. The AO desktop already builds a
per-platform Node 22.23.2 runtime, so no user-installed Node is needed. Expo
Device Hub declares no top-level engine, while its pinned serve-sim revision
documents Node 20+.

The Expo tarball's native files were inspected directly:

| Artifact | Mach-O target | Minimum OS | Finding |
| --- | --- | --- | --- |
| `serve-sim-native.node` | macOS arm64 | macOS 14.0 | Required for iOS native capture; ad-hoc signed upstream |
| `LiveKitWebRTC` | macOS arm64 | macOS 11.0 | Runtime framework; ad-hoc signed upstream |
| `serve-sim-camera-helper` | macOS arm64 | macOS 14.0 | Optional camera injection helper |
| `serve-sim-ax-settings` | iOS Simulator arm64 | iOS 15.0 | Runs inside the simulator |
| `libSimCameraInjector.dylib` | iOS Simulator arm64 | iOS 15.0 | Optional camera injection payload |
| `node_datachannel.node` from the audit install | macOS arm64 | macOS 14.0 | Native WebRTC dependency; produced by an npm install script |

This is intentional upstream behavior, not a bad local download. The exact
serve-sim source declares `cpu: ["arm64"]`, documents Apple Silicon as a
requirement, and has tests that reject non-arm64 packages. Expo's outer
`expo-device-hub` manifest does not carry that CPU constraint even though it
vendors those binaries. AO must therefore add its own packaging and runtime
architecture checks.

AO currently publishes separate `darwin-arm64` and `darwin-x64` artifacts. The
device runtime must be an optional, target-specific resource so an unsupported
helper can never make the Intel app fail to package, sign, start, or update.
The honest initial capability matrix is:

| AO host | iOS Simulator | Android Emulator | Rest of AO |
| --- | --- | --- | --- |
| Apple Silicon, macOS 14+ | Candidate, pending live E2E | Candidate, pending live E2E | Unchanged |
| Apple Silicon, older macOS | Unavailable: native minimum OS | Unavailable for the audited live runtime | Unchanged |
| Intel macOS | Unavailable until a compatible iOS transport exists | May be technically possible, but not release-verified in this stack | Unchanged |
| Windows / Linux | Not in the first release | Later adapter | Unchanged |

## Installation and packaging findings

A temporary install completed and both CLI help paths ran. The npm client
reported that `node-datachannel@0.32.3` has this install script:

```text
prebuild-install -r napi || (npm install --ignore-scripts --production=false && npm run _prebuild)
```

The installed native addon imported successfully on the arm64 audit host.
Nevertheless, arbitrary dependency install scripts must remain disabled. The
feature packaging step must explicitly allow only this reviewed package, then
assert the resulting target architecture, minimum OS, and loadability with the
packaged Node runtime.

Recommended packaging rules for the feature PR:

1. Add a dedicated, committed runtime manifest and lockfile containing the two
   direct versions and their full transitive graph.
2. Build with AO's normal artifact job on the target architecture; never
   install at end-user runtime.
3. Allow only the reviewed `node-datachannel` install script.
4. Copy a minimal runtime to `Contents/Resources/device-runtime` (or the
   equivalent platform resource directory) with all required licenses/notices.
5. Generate and verify a SHA-256 manifest for entry scripts, native modules,
   frameworks, helper binaries, and APK payloads.
6. Fail packaging if any executable has the wrong architecture, unexpected
   minimum OS, missing dependency, missing license, or unresolved dynamic
   library.
7. Re-sign all nested Mach-O code with AO's release identity before the outer
   app seal, then use the existing canonical macOS artifact verifier after
   packaging and notarization.
8. Assert the runtime is absent or capability-disabled on unsupported AO
   targets without affecting application startup.

The feature may share AO's pinned Node distribution, but it should not couple
the Go device service to an "ACP-only" filesystem detail. The packaging layer
must expose a stable internal Node-runtime path used by both subsystems, or
document and test the existing resource path as a shared runtime contract.

## Runtime smoke evidence

Audit host:

```text
macOS 26.4 arm64
Node 26.8.1 for the temporary install/smoke
Xcode Command Line Tools only; no simctl
Android SDK/emulator tools absent
```

Observed results:

- `expo-device-hub --help` and `agent-device --help` completed.
- Expo Device Hub started on the explicitly supplied `127.0.0.1` address and
  answered `/api/devices` with HTTP 200.
- With platform tools absent, its response contained raw command failures,
  absolute user paths, command lines, and JavaScript stack traces. AO must map
  these to bounded codes such as `XCODE_REQUIRED` and `ANDROID_SDK_REQUIRED`;
  the raw body must never be returned directly or persisted as telemetry.
- Agent Device started its HTTP daemon on a random `127.0.0.1` port. Its
  `daemon.json` mode was `0600` and contained a random token. `/rpc` returned
  HTTP 401 for both missing and invalid bearer tokens. `/health` was readable
  without a token.
- Agent Device stopped cleanly through its daemon command.

The smoke confirms basic process and loopback behavior only. It does not prove
frame streaming, input, UI snapshots, concurrent ownership, crash recovery, or
packaged-app code signing.

## Lessons from the T3 reference

T3 Code PR #10677 proves that the two helpers can support one coherent local
device experience, including an agent path, in a substantial product PR. It
does not prove that the change was independent: the PR was based on the shared
wizard foundation in [#10832](https://github.com/pingdotgg/t3code/pull/10832),
while multi-host and SSH work was stacked above it in
[#10854](https://github.com/pingdotgg/t3code/pull/10854)-[#10856](https://github.com/pingdotgg/t3code/pull/10856).
AO follows the same dependency discipline by putting this
non-feature prerequisite below one complete macOS feature PR and keeping later
host adapters above it.

The reusable ideas are the split between streaming and agent automation,
loopback helper processes, explicit readiness, stale-process cleanup, a strict
same-origin allowlist, and separate consent/capability state. AO intentionally
does not copy T3's runtime npm installation: the requested AO product bundles
the helpers, and AO's release process already has architecture, signing,
notarization, integrity, and offline-start requirements that are better checked
at build time.

T3's route comments also confirm why a raw proxy is unacceptable: serve-sim
has an exec surface and serve-emu action routes are not independently
authenticated. The helper origin is an implementation detail even when it
binds loopback.

## Existing AO Android experiment

AO PR #3882 remains useful implementation evidence for Android SDK detection,
emulator process-tree supervision, readiness/crash behavior, gRPC input/frame
transport, UI integration, and real-device failure cases. At audit time it was
an open 126-file change with 21,579 additions, an Android-specific public API,
AO-managed SDK downloads, and no iOS implementation.

The new feature should reuse focused tests and platform lessons from that PR,
not merge it wholesale or preserve its public `/android-device` contract. In
particular, the following findings remain applicable regardless of helper:

- an emulator launcher PID is not the entire process tree;
- readiness must race process exit and retain the last bounded diagnostic;
- stale AVD locks can survive a crash;
- Android ABI names and emulator CPU architecture names are not interchangeable;
- a tap requires both touch-down and touch-up;
- long-running setup must not inherit an HTTP request's cancellation context.

SDK download/install and AO-owned AVD creation are outside the newly fixed
scope. The neutral `/devices` service and existing-SDK-only behavior supersede
those parts of #3882.

## Security findings

### Expo Device Hub

The standalone CLI defaults to `127.0.0.1`, but accepts `--host 0.0.0.0` and
does not authenticate callers. Its vendored surface includes more authority
than AO needs, including serve-sim exec/WebSocket behavior and device/app
management routes. AO must always pass an explicit loopback host and must proxy
a positive allowlist rather than a path prefix or the full origin.

At minimum, the feature review must prove:

- no helper origin, port, PID, or token reaches a renderer response;
- HTTP method checks happen before forwarding;
- input WebSockets require the same AO session authority as REST mutations;
- cookies, AO bearer tokens, host, origin, and hop-by-hop headers do not leak
  upstream;
- redirects and unknown paths are not forwarded;
- the LAN listener returns its ordinary 404 envelope for every device prefix.

### Agent Device

The daemon's HTTP mode is loopback and bearer-protected for RPC, but its RPC
catalog is much broader than AO's first release. AO must use a private adapter
with a fixed mapping for the selected commands. It must not expose `/rpc`,
upload/download routes, MCP, remote providers, arbitrary output paths, or the
agent-device binary to the worker.

Set `AGENT_DEVICE_NO_UPDATE_NOTIFIER=1` and do not enable cloud authentication,
remote configuration, companion tunnels, or update checks. The Go service must
redact helper diagnostics and bound returned accessibility/screenshot data.

## State-location findings

`AGENT_DEVICE_STATE_DIR` relocates the daemon's primary state, but source
inspection found other defaults under `~/.agent-device`, including device
claims, Apple runner leases/derived products, configuration, helper/cache, and
some diagnostic paths. The feature must set every applicable supported
override beneath `<AO_DATA_DIR>/devices`:

- `AGENT_DEVICE_STATE_DIR`;
- `AGENT_DEVICE_CLAIMS_DIR`;
- `AGENT_DEVICE_IOS_RUNNER_LEASE_DIR`;
- `AGENT_DEVICE_IOS_RUNNER_DERIVED_PATH`;
- `AGENT_DEVICE_SWIFT_CACHE_DIR`;
- `AGENT_DEVICE_CONFIG` when config loading cannot be disabled.

The exact list is not assumed complete. A feature-PR confinement test must run
the real helper workflow with filesystem observation and fail if any new path
outside the AO root appears. If an unavoidable active code path has no override,
AO must patch upstream, contribute the override, or defer that capability.

Temporary files created by system libraries remain subject to OS conventions,
but durable AO-owned state, caches, snapshots, and helper products do not.

## AO architecture fit

The helpers fit only behind these existing boundaries:

- Go daemon service for policy and lifecycle;
- ports/adapters for helper processes and platform probes;
- code-first HTTP DTOs and generated OpenAPI/frontend types;
- session-bound capability verifier modeled on `service/browser/authority.go`;
- thin Cobra client over daemon HTTP;
- thin renderer over the generated client;
- embedded `using-ao` command documentation added with the CLI;
- `<AO_DATA_DIR>` for all owned state;
- `lanControlBlockedPrefixes` for the entire device API.

The public DTOs must not contain Expo routes, agent-device flags, `simctl`
payloads, ADB payloads, or Node concepts. That keeps a later Windows/Linux
Android adapter and a future helper replacement possible without a client
redesign.

## Release gates for the feature PR

The audited stack is approved for implementation only after the feature PR
proves all of the following:

- [ ] Exact locked dependency graph and integrity manifest are checked in.
- [ ] Third-party license/notice bundle is complete, including MPL-2.0 handling.
- [ ] Target architecture and minimum-OS checks fail packaging closed.
- [ ] Nested native code survives AO signing, notarization, and artifact verification.
- [ ] Packaged Node, not system Node/npm, starts both helpers.
- [ ] Every helper socket is loopback-only and internal to the daemon.
- [ ] Expo proxy and Agent Device RPC allowlists have negative-path tests.
- [ ] Every device route is physically unreachable from the LAN listener.
- [ ] Agent capabilities rotate and cannot cross sessions.
- [ ] Real helper workflows write durable data only under `AO_DATA_DIR`.
- [ ] Missing SDKs return bounded, display-safe capability diagnostics.
- [ ] Real Apple Silicon macOS E2E covers iOS and Android discovery, boot,
      stream, screenshot, UI tree, input, close, shutdown, and daemon restart.
- [ ] Intel and unsupported-OS builds keep all non-device AO functionality and
      report explicit unsupported capability.

Any unchecked item is a release blocker, not follow-up polish.
