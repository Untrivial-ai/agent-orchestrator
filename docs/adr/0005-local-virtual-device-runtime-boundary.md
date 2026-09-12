# 5. An AO-owned boundary for local virtual devices

Date: 2026-09-13
Status: Proposed

## Context

AO needs a macOS-only first release that can discover, boot, display, inspect,
and control existing iOS Simulators and Android Emulators. The same operations
must be available to the person using the desktop app and to the coding agent
in that AO session.

The platform tooling already exists: Xcode owns CoreSimulator and `simctl`, and
the Android SDK owns `adb`, `emulator`, and AVDs. Reimplementing live capture,
input injection, and accessibility traversal would add substantial native code
without improving AO's product boundary. Conversely, exposing a third-party
device server or CLI directly would bypass AO's session isolation, error
envelopes, LAN restrictions, and process lifecycle.

The prerequisite audit in
[the device runtime evidence note](../research/2026-09-13-local-device-runtime-prerequisites.md)
evaluates the exact upstream artifacts. The implementation plan in
[the local device support design](../superpowers/specs/2026-09-13-local-device-support-design.md)
defines the first user-visible PR.

## Decision

AO owns the public device contract and delegates narrowly defined mechanics to
pinned open-source helpers.

```text
React device panel          ao device
         |                      |
         +---- typed daemon API-+
                        |
              Go device service
              policy / auth / lifecycle
                 |             |
       allowlisted proxy     typed RPC adapter
                 |             |
       expo-device-hub      agent-device
          live stream       inspect + act
                 \             /
                  existing Xcode / Android SDK
```

The Go daemon owns:

- platform-neutral device, session, capability, and action types;
- capability discovery and display-safe diagnostics;
- session-scoped authorization and device ownership;
- helper startup, readiness, restart, teardown, and stale-process recovery;
- every route and operation allowlist exposed to the renderer or CLI;
- validation, timeouts, size limits, rate limits, and API error mapping.

The renderer remains a thin client. The `ao device` CLI is another thin client
to the same daemon routes; it never invokes `simctl`, `adb`, an emulator, or a
helper directly.

### Selected helpers

The first implementation will evaluate and pin these exact versions behind the
AO boundary:

- `expo-device-hub@0.9.0` for live iOS/Android display transport and basic
  pointer input;
- `agent-device@0.20.10` for token-efficient accessibility snapshots and typed
  device actions.

The helpers are build-time dependencies of the desktop artifact, not software
downloaded with `npx` or installed from the registry on first use. Their entire
resolved npm graph must be locked and integrity checked. AO runs them with its
pinned Node 22 runtime and does not depend on a user's Node installation.

Xcode, iOS Simulator runtimes, Android SDK tools, Android system images, and
AVDs are **not** bundled or installed by AO. Capability discovery points the
user to Xcode or Android Studio when one is missing.

This is conditional approval, not permission to package the audited tarballs
unchanged. The audit found architecture, minimum-OS, code-signing, and state
confinement gates that the feature PR must close.

### Supported host contract

The initial verified target is Apple Silicon macOS 14 or newer. AO continues to
build and work on Intel macOS, but the iOS streaming artifact in
`expo-device-hub@0.9.0` is intentionally arm64-only. The feature must report a
clear unavailable capability on an unsupported host; it must never attempt to
load the wrong Mach-O binary or break the rest of AO.

Intel device support requires a separately reviewed backend, such as an
AO-owned `simctl` screenshot transport, or upstream universal/x64 artifacts.
It is not achieved by copying or translating the arm64 binaries.

Windows and Linux keep only the neutral daemon seam in this release. They do
not advertise device support. Later Android adapters may implement the same
contract without changing the renderer or agent command vocabulary.

### Security and network boundary

Both helpers bind random ports on `127.0.0.1` only. Their ports and credentials
stay inside the daemon and are never returned to the renderer or an agent.

`expo-device-hub` is unauthenticated and includes vendor routes that can execute
commands. AO therefore does not publish its origin wholesale. A daemon-owned
reverse proxy forwards only the exact read, stream, and input routes required
by the product, constrains methods, strips credentials and hop-by-hop headers,
and rejects every dashboard, shell/exec, application-install, remote, and
unrecognized route.

`agent-device` runs in loopback HTTP mode with a random bearer token stored in
an AO-owned `0600` state file. Only the Go adapter holds that token. It sends a
fixed catalog of typed requests and does not expose the helper's CLI, MCP
server, generic RPC method, upload endpoints, cloud providers, or arbitrary
flags.

All `/api/v1/devices` routes are added to the LAN listener's outer
`lanControlBlockedPrefixes` gate. This remains true even though the opt-in LAN
listener has bearer authentication: local virtual-device control is a desktop
and local-agent capability, not a Connect Mobile capability.

Agent requests use the existing browser-capability pattern: `AO_SESSION_ID`
selects the owning session, `AO_DEVICE_CAPABILITY` carries an unguessable token,
and `X-AO-Device-Capability` presents it to the daemon. AO stores only a
session-bound verifier and rotates the capability with each supervised worker
generation. The renderer uses its privileged desktop bridge and cannot mint
agent capabilities.

### State and process boundary

Every helper state, cache, claim, lease, log, and temporary path must resolve
under `<AO_DATA_DIR>/devices`. The feature sets every supported agent-device
path override explicitly and adds an integration test that fails on a write
outside the AO-owned root. A helper defaulting to `~/.agent-device` is a release
blocker, not an exception to AO's state-location rule.

AO records enough PID identity to distinguish an owned helper from a recycled
PID before cleanup. It terminates only helpers it started, waits with bounded
timeouts, escalates process-tree termination when required, and treats failed
or unknown probes as unknown rather than proof that a device is dead. AO does
not claim ownership of Xcode's Simulator processes, Android Studio, `adb`, or
emulators that the user started elsewhere.

Transient frames and display status are not durable state. Device availability
and running state are derived from live helper/platform facts. Durable storage
is limited to consent/settings and session capability verifiers if restart
recovery requires them.

### Scope of the first user-visible device PR

The first feature PR includes both human and agent access to:

- status and missing-prerequisite diagnostics;
- device discovery;
- boot/open, attach, close, and explicit device shutdown;
- live view or the explicitly documented fallback transport;
- screenshot capture;
- accessibility/UI-tree inspection;
- tap, swipe, text, key, and back/home actions supported by the target.

It also adds `ao device ...` commands and the matching embedded
`using-ao/commands/device.md` page in the same PR. There is no partial release
where the panel can control a device but an authorized agent cannot.

The first feature PR does not install SDKs or simulator runtimes, create AVDs,
build projects, install application packages, automate signing/provisioning,
control physical devices, or add remote/SSH device hosts.

## Consequences

AO gets mature native mechanics without making a Node helper its product API.
The daemon can replace either helper later while retaining its public DTOs and
agent workflow.

The desktop artifact grows by roughly 49 MB unpacked for the audited combined
npm tree before packaging compression. Native resources must participate in
AO's per-architecture build, signing, notarization, license, and artifact
verification gates.

The initial support matrix is narrower than "all Macs." That is preferable to
silently shipping arm64 binaries in AO's Intel build. A future Intel backend
can be added behind the same capability model.

AO becomes responsible for tracking two upstream releases and their transitive
dependency/license changes. Version bumps require rerunning the prerequisite
audit, reviewing route and filesystem behavior, and updating the lockfile and
integrity manifest together.
