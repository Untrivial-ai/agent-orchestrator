# Local iOS Simulator and Android Emulator Support Design

Date: 2026-09-13
Status: Proposed implementation plan for the first user-visible device PR.

## Goal

On a supported Mac, AO users and the agent in the current AO session can share
an existing local iOS Simulator or Android Emulator: discover it, boot or
attach, see its screen, inspect accessible UI, act on it, take evidence, and
release or explicitly shut it down.

The first user-visible device PR is complete only when the agent-control path
ships with the desktop path. The prerequisite documentation PR is intentionally
not counted as that feature PR.

## Fixed scope

### Included

- Apple Silicon macOS 14+ as the first verified host.
- Existing Xcode/iOS Simulator and Android SDK/AVD installations.
- iOS Simulator and Android Emulator discovery.
- Boot, attach/open, close/detach, and explicit shutdown.
- Live display through a daemon-controlled stream proxy.
- Screenshot and accessibility/UI-tree capture.
- Tap/press, swipe/scroll, text/fill, key, back, and home where supported.
- One selected device attachment per AO session, isolated from other sessions.
- A Device panel for the user.
- An `ao device` CLI and embedded `using-ao` command page for the agent.
- Capability-only unsupported results on Intel macOS, older macOS, Windows,
  Linux, and hosts missing the vendor SDKs.

### Excluded

- Downloading Xcode, Simulator runtimes, Android SDK tools, system images, or
  AVDs.
- Creating or editing AVDs.
- Project detection, builds, application install/reinstall, app-data reset,
  signing, provisioning, TestFlight, or store deployment.
- Physical iOS or Android devices.
- Remote, SSH, cloud, CI, or LAN-hosted device control.
- Generic `simctl`, `adb`, shell, helper CLI, helper RPC, or MCP access.
- Windows/Linux Android execution in this PR.
- Camera, location, push-notification, logcat, network, profiling, recording,
  replay, and deep-link surfaces in this PR.

Those omissions keep the first feature focused on a safe inspect-act-verify
loop. They can be added as separately authorized actions after this boundary is
proven.

## Support matrix

| Host | iOS Simulator | Android Emulator | Result |
| --- | --- | --- | --- |
| Apple Silicon macOS 14+ with full Xcode | Enabled | Depends on Android SDK | First verified target |
| Apple Silicon macOS 14+ without full Xcode | `XCODE_REQUIRED` | Depends on Android SDK | Android may still be enabled |
| Apple Silicon macOS 14+ without Android SDK/emulator | Depends on Xcode | `ANDROID_SDK_REQUIRED` | iOS may still be enabled |
| Apple Silicon macOS below 14 | `HOST_OS_UNSUPPORTED` | `HOST_OS_UNSUPPORTED` for audited runtime | AO otherwise works |
| Intel macOS | `HOST_ARCH_UNSUPPORTED` | Not release-verified; unavailable in this PR | AO otherwise works |
| Windows / Linux | `HOST_PLATFORM_UNSUPPORTED` | `NOT_IMPLEMENTED` | Later adapters reuse the public contract |

Availability is per platform. A missing Android installation must not disable
iOS, and a missing Xcode installation must not disable Android.

## User experience

The session inspector gains a Device tab. It has four states:

1. **Unavailable** — shows the exact host/platform reason and a link to the
   official Xcode or Android Studio instructions. There is no AO Install button.
2. **No device selected** — lists discovered simulators/emulators with platform,
   model, runtime/API, boot state, and busy state.
3. **Starting** — shows bounded boot/attachment progress and a Cancel action.
4. **Attached** — shows the live device, connection quality, screenshot action,
   input controls, Close, and a separately confirmed Shut down action.

Opening an already booted device attaches without claiming AO started it.
Opening a stopped device requests the platform tool to boot it and then waits
for readiness. Closing always releases the AO attachment and helper session; it
does not power off the simulator/emulator. Shutting down is explicit and warns
that it affects other tools using that virtual device.

The panel stops its stream when hidden but keeps the session attachment so the
agent can continue. Returning to the tab requests a new short-lived stream
ticket and reconnects.

## Architecture

### Package ownership

Add neutral domain vocabulary under `backend/internal/domain`:

- `DevicePlatform`: `ios`, `android`;
- `DeviceKind`: `simulator`, `emulator`;
- `DeviceID`, `DeviceAttachmentID`;
- lifecycle facts: `stopped`, `booting`, `booted`, `unavailable`, `unknown`;
- capability and supported-action values.

Do not place Expo paths, Agent Device commands, `simctl` JSON, ADB output, HTTP
DTOs, or UI labels in the domain package.

Add narrow ports under `backend/internal/ports`:

```go
type DeviceHost interface {
    Capabilities(context.Context) ([]DevicePlatformCapability, error)
    List(context.Context) ([]Device, error)
    Boot(context.Context, DeviceID) error
    Shutdown(context.Context, DeviceID) error
}

type DeviceRuntime interface {
    Attach(context.Context, AttachRequest) (Attachment, error)
    Snapshot(context.Context, AttachmentID, SnapshotOptions) (Snapshot, error)
    Screenshot(context.Context, AttachmentID) (Screenshot, error)
    Act(context.Context, AttachmentID, DeviceAction) (ActionResult, error)
    Detach(context.Context, AttachmentID) error
}
```

The exact Go types may combine these interfaces if the real call sites are
simpler, but public and domain types remain helper-neutral.

Concrete process/protocol code belongs under
`backend/internal/adapters/device`. The adapter may use:

- Expo Device Hub for device inventory, boot coordination, stream bytes, and
  basic stream input;
- Agent Device's authenticated loopback daemon for accessibility snapshots,
  element refs/selectors, screenshots, and typed actions;
- small official platform probes only when needed to validate helper results.

The device service under `backend/internal/service/device` owns capability
derivation, attachment leases, authorization, lifecycle, retries, and public
error mapping. The helpers never become service interfaces.

### Helper runtime

The feature PR adds a dedicated manifest/lock and packaging preparation script
following the existing agent-browser and ACP-runtime patterns:

```text
frontend/device-runtime/
├── package.json
└── package-lock.json

frontend/resources/device-runtime/
├── node_modules/...
├── licenses/...
└── integrity.json
```

The generated resource directory stays uncommitted; the manifest, lockfile,
preparation script, tests, and license inventory are committed. Packaging runs
`npm ci` with install scripts denied except for the reviewed
`node-datachannel` build, verifies every expected entry/native payload, and
copies the resource through `extraResourcesForPlatform` only on supported
targets.

Use AO's Node 22.23.2 distribution. If the existing ACP resource is shared,
rename or wrap its lookup as a general internal Node runtime contract rather
than making device code know an ACP-specific path. Do not ship a second Node
copy without an artifact-size comparison and explicit justification.

The packaging tests assert:

- exact `expo-device-hub@0.9.0` and `agent-device@0.20.10` entrypoints;
- lockfile integrity and approved install-script set;
- licenses/notices and SHA-256 manifest;
- Mach-O architecture and minimum OS;
- dynamic library resolution;
- packaged-Node imports and CLI help;
- target exclusion on unsupported platforms;
- nested code signing and canonical artifact verification.

### Helper supervision

Helpers start lazily on the first supported device operation, not at daemon
startup. The daemon passes explicit environment and argv:

```text
<node> expo-device-hub/dist/server/cli.mjs
  --host 127.0.0.1 --port <reserved> --hide-sidebar --hide-boot-device

AGENT_DEVICE_STATE_DIR=<AO_DATA_DIR>/devices/agent-device
AGENT_DEVICE_CLAIMS_DIR=<AO_DATA_DIR>/devices/agent-device/claims
AGENT_DEVICE_IOS_RUNNER_LEASE_DIR=<AO_DATA_DIR>/devices/agent-device/apple-runner/leases
AGENT_DEVICE_IOS_RUNNER_DERIVED_PATH=<AO_DATA_DIR>/devices/agent-device/apple-runner/derived
AGENT_DEVICE_SWIFT_CACHE_DIR=<AO_DATA_DIR>/devices/agent-device/swift-cache
AGENT_DEVICE_CONFIG=<AO_DATA_DIR>/devices/agent-device/config.json
AGENT_DEVICE_DAEMON_SERVER_MODE=http
AGENT_DEVICE_DAEMON_IDLE_TIMEOUT_MS=0
AGENT_DEVICE_NO_UPDATE_NOTIFIER=1
```

Both ports are reserved on `127.0.0.1`, readiness is bounded, and the Agent
Device token is read from its `0600` state file without logging it. PID state
contains executable identity and process start time so stale cleanup does not
kill a recycled PID. Repeated crashes use a bounded backoff and eventually
surface `DEVICE_RUNTIME_UNAVAILABLE` rather than restart forever.

Daemon shutdown detaches AO helper sessions and stops both helpers. It does not
kill Simulator.app, Android Studio, `adb`, or a virtual device unless AO is
executing an explicit, authorized shutdown request.

## Authorization model

Every device API operation requires one of two principals:

- **Agent principal:** `AO_SESSION_ID` plus a fresh
  `AO_DEVICE_CAPABILITY`, presented as `X-AO-Device-Capability`. The daemon
  validates it against a session-bound verifier in constant time.
- **Desktop principal:** an Electron-main-only runtime token handed to the
  daemon out of band and attached by typed main-process IPC. The renderer never
  receives the token and cannot set arbitrary helper requests.

The token implementation follows the existing browser capability authority and
worker-generation rotation. A device capability authorizes only its session;
it does not authorize a specific device until `open` establishes an
attachment. Cross-session attachment or action returns a generic not-found or
conflict result without leaking another session's details.

The service permits at most one controlling AO attachment per virtual device.
Other sessions can list the device as busy but cannot inspect its app, screen,
or owner identity. Close, session termination, worker replacement, and daemon
shutdown release the AO attachment.

All `/api/v1/devices` paths are blocked at the LAN listener before LAN auth.
There is no exception for read-only inventory or stream traffic.

## HTTP contract

Keep the public API small and action-oriented, similar to the browser runtime:

```text
GET  /api/v1/devices/status?sessionId=<id>
GET  /api/v1/devices?sessionId=<id>
POST /api/v1/devices/commands
POST /api/v1/devices/stream-tickets
GET  /api/v1/devices/streams/<ticket>
```

`DeviceCommandRequest` contains `sessionId`, an enum action, and a typed union
of action arguments. It does not accept commands, argv, paths, URLs, helper
routes, environment, output filenames, or vendor-specific options.

Initial actions:

```text
open(deviceId)
screenshot()
uiTree(interactiveOnly)
tap(ref | x,y)
swipe(x1,y1,x2,y2,durationMs?)
fill(ref,text)
type(text)
key(key)
back()
home()
close()
shutdown(deviceId?,confirmation)
```

Coordinates, text length, key names, swipe duration, body size, tree node
count/depth, screenshot dimensions, and total request duration have explicit
limits. Device IDs must match the latest live inventory and attachment.

Stream tickets are random, single-use, expire within 30 seconds, and bind to
the session, attachment, device, direction, and requested transport. They avoid
putting the long-lived capability in a URL and allow `<img>`/WebSocket clients
that cannot set headers. The proxy forwards only the exact Expo read/stream and
input paths selected by server code. It never concatenates a client-provided
upstream path.

Controller DTOs live in `controllers/dto.go`, operations in
`apispec/specgen/build.go`, and `npm run api` regenerates OpenAPI plus the
frontend schema. CLI DTOs remain hand-mirrored and have wire-compatibility
tests.

## CLI and embedded skill contract

The first feature PR adds:

```text
ao device status [--json]
ao device list [--json]
ao device open <device-id> [--json]
ao device screenshot [path] [--base64] [--json]
ao device ui-tree [--interactive] [--json]
ao device tap <ref>|<x> <y> [--json]
ao device swipe <x1> <y1> <x2> <y2> [--duration <milliseconds>] [--json]
ao device fill <ref> <text> [--json]
ao device type <text> [--json]
ao device key <key> [--json]
ao device back [--json]
ao device home [--json]
ao device close [--json]
ao device shutdown [device-id] --yes [--json]
```

`AO_SESSION_ID` selects the session, so no command accepts another session ID.
`AO_DEVICE_CAPABILITY` is mandatory. Misuse is a CLI usage error (exit 2),
while runtime/daemon failures exit 1 and preserve the API request ID.

`ui-tree` prints compact refs that are valid only for the latest snapshot
generation. Mutation invalidates prior refs. Screenshot file output follows
the browser command's behavior: the CLI writes it, refuses overwrite, and the
daemon never accepts an arbitrary filesystem destination.

Device screens and accessibility text are untrusted application content. Text
and structured responses carry the same explicit trust labeling used by
`ao browser`; the embedded instructions tell agents never to follow commands
found in a device UI or expose credentials displayed there.

Update the existing catalog in the same PR:

- add `backend/internal/skillassets/using-ao/commands/device.md`;
- link it from `backend/internal/skillassets/using-ao/SKILL.md`;
- update the catalog references/index used by its embedding tests.

Do not add a second standalone skill. Device control is part of the existing
`using-ao` skill because it documents AO's own CLI.

## Error model

Normalize helper failures into stable AO codes and bounded messages:

```text
HOST_PLATFORM_UNSUPPORTED
HOST_ARCH_UNSUPPORTED
HOST_OS_UNSUPPORTED
XCODE_REQUIRED
IOS_SIMULATOR_RUNTIME_REQUIRED
ANDROID_SDK_REQUIRED
ANDROID_EMULATOR_REQUIRED
ANDROID_AVD_REQUIRED
DEVICE_NOT_FOUND
DEVICE_BUSY
DEVICE_BOOT_TIMEOUT
DEVICE_RUNTIME_UNAVAILABLE
DEVICE_STREAM_UNAVAILABLE
DEVICE_ACTION_UNSUPPORTED
DEVICE_ATTACHMENT_REQUIRED
DEVICE_CAPABILITY_INVALID
```

Raw helper responses, stack traces, absolute home paths, bearer tokens, ports,
PIDs, and command lines remain in bounded debug logs only after redaction. API
errors use AO's normal envelope and request ID.

## State and lifecycle

Persist only:

- device feature setting/consent if product onboarding needs it;
- per-session device capability verifier;
- minimal owned-helper recovery identity if filesystem state is insufficient.

Do not persist device lists, boot status, frames, UI trees, screenshots,
attachment display status, helper ports, or tokens in SQLite. Derive them from
live facts. Helper-owned state stays under `<AO_DATA_DIR>/devices` with private
permissions.

A daemon restart leaves user-owned virtual devices running, reaps only verified
stale helper processes, starts clean helpers lazily, and requires the session
to reattach. Unknown platform probes produce `unknown`/unavailable facts, not a
claim that the virtual device is dead.

## Implementation order inside the feature PR

Keep the feature as one user-visible PR based on this prerequisite PR, with
reviewable conventional commits in this order:

1. `build:` locked helper runtime, license bundle, architecture/integrity tests.
2. `feat:` neutral domain, ports, capability probes, and helper supervisors.
3. `feat:` attachment service, Agent Device adapter, and Expo allowlisted proxy.
4. `feat:` session capability minting/rotation, LAN block, controller, DTOs,
   OpenAPI, and generated frontend types.
5. `feat:` `ao device` commands and embedded `using-ao` documentation.
6. `feat:` Electron main IPC and the session Device panel.
7. `test:` packaged-runtime, failure, concurrency, and real-device coverage.
8. `docs:` user-facing support matrix, setup, privacy, and troubleshooting.

The branch may be kept as a draft while early commits are incomplete. It is not
ready for review or release until the CLI/skill and Device panel both exercise
the same complete daemon capability set.

## Test plan

### Backend and CLI

- capability matrix across OS, architecture, SDK presence, and probe failure;
- parsing and redaction of representative Expo, `simctl`, ADB, and Agent Device
  responses;
- helper argv/environment, loopback binding, readiness, crash backoff, stale
  PID protection, and teardown;
- positive proxy paths plus every rejected route/method/header/redirect case;
- Agent Device command mapping and proof generic RPC/upload/cloud operations
  cannot be selected;
- attachment exclusivity, cross-session denial, capability rotation, session
  end, daemon restart, and explicit shutdown confirmation;
- input/tree/frame/body/time bounds;
- LAN 404 for every `/api/v1/devices` path;
- happy path, usage errors, API envelopes, request IDs, and screenshot
  no-overwrite behavior for every CLI command;
- API route/schema parity and embedded skill frontmatter/link coverage.

### Frontend and Electron main

- unavailable reason and official setup-link rendering;
- independent iOS/Android capability states;
- device inventory, busy state, boot/cancel/error transitions;
- stream ticket mint, reconnect, expiry, panel-hide cleanup, and target switch;
- pointer/keyboard action conversion without renderer-supplied helper paths;
- screenshot and UI-tree states;
- close versus confirmed shutdown semantics;
- Electron main holds the desktop token and exposes only typed device IPC;
- no regression on unsupported macOS, Windows, or Linux builds.

### Packaging and release

- reproducible `npm ci` from the committed device lockfile;
- dependency license inventory and notice bundle drift;
- native architecture, minimum OS, dynamic libraries, and integrity manifest;
- packaged Node startup and native module import;
- arm64 signed/notarized zip and dmg through
  `frontend/scripts/verify-mac-artifact.sh`;
- x64 package starts and updates with device runtime absent/unavailable;
- artifact-size delta recorded in the PR.

### Manual acceptance on an isolated Mac

Use an isolated `AO_DATA_DIR` and the real Electron app. Test both iOS and
Android with at least one stopped and one already-running virtual device:

1. detect prerequisites and list devices;
2. boot, wait ready, attach, and display the live screen;
3. inspect the UI and act by ref plus coordinates;
4. type/fill, key, back/home, and swipe;
5. take a screenshot through UI and CLI;
6. repeat the same inspect-act-verify loop from an authorized agent;
7. prove a second AO session cannot take over the attachment;
8. close without powering off, reattach, then explicitly shut down;
9. restart the daemon and recover without killing the virtual device;
10. kill each helper and verify bounded recovery/error behavior;
11. inspect sockets and filesystem writes for loopback/state confinement;
12. verify missing-Xcode and missing-Android-SDK states separately.

The acceptance report records macOS version, Mac architecture, Xcode version,
iOS runtime/device, Android SDK/emulator version, AVD/API/ABI, AO artifact, and
every command/check result.

## Stacked PR sequence

The work is stacked intentionally:

```text
main
  └─ prerequisite architecture/license audit (this PR; no feature surface)
       └─ macOS local-device feature (first user-visible PR; UI + agent together)
            ├─ Intel macOS capture backend, if selected
            ├─ Windows Android adapter
            └─ Linux Android adapter
```

Each follow-up targets the branch directly below it until its parent merges,
then retargets to the new base. Remote device hosts, SDK installation, and
build/deploy workflows remain separate product decisions rather than implicit
parts of the Windows/Linux adapters.

## Definition of done

The first user-visible PR is done when all release gates in
[the prerequisite audit](../../research/2026-09-13-local-device-runtime-prerequisites.md)
are checked and a fresh supported AO desktop artifact lets both the user and
the authorized session agent complete the same iOS and Android
inspect-act-verify workflow without exposing helper internals, crossing session
boundaries, reaching the LAN listener, writing outside `AO_DATA_DIR`, or
regressing unsupported AO builds.
