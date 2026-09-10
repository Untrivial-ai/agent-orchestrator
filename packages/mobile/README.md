# Agent Orchestrator — Mobile

Expo (expo-router) mobile supervisor for Agent Orchestrator. Four tabs — Kanban, PRs,
Orchestrator, Settings — plus a Chat-first spawn flow, a native conversation surface,
the existing live terminal, and a preview browser. Chat sessions expose durable history,
streaming activity, approvals, provider controls, attachments, voice input, and a plain
worktree-shell escape hatch. It is a **thin client**: it talks to the AO daemon running on your
computer over your local network (or Tailscale). It never runs agents itself.

> **Development builds only — Expo Go is not supported.** The app depends on native modules,
> so it must be compiled onto the device (`npx expo run:ios|run:android`). Expo Go can't load
> it; don't file bugs from it.

- [How the phone reaches your machine](#how-the-phone-reaches-your-machine)
- [Prerequisites](#prerequisites)
- [Install](#install)
- [Step 1 — Turn on Connect Mobile on the desktop](#step-1--turn-on-connect-mobile-on-the-desktop)
- [Step 2 — Build and install the dev build](#step-2--build-and-install-the-dev-build)
- [Step 3 — Pair the phone](#step-3--pair-the-phone)
- [Everyday dev loop](#everyday-dev-loop)
- [Troubleshooting](#troubleshooting)
- [Project layout](#project-layout)
- [Verify](#verify)

## How the phone reaches your machine

The daemon's primary listener is loopback-only (`127.0.0.1:3001`) and unauthenticated — a
phone can never reach it. To let a phone in, the desktop app opens a **second, opt-in LAN
listener** (default port **3011**) bound to `0.0.0.0`, protected by a rotating bearer
password, serving only the app API. It exists only while **Connect Mobile** is switched on
in the desktop app; switching it off closes the socket.

```
phone ──HTTP/WS── 0.0.0.0:3011   (LAN listener, bearer password, opt-in)
                       │
                 same daemon process
                       │
desktop/CLI ───── 127.0.0.1:3001 (loopback, no auth, unchanged)
```

Transport is **plaintext HTTP by design** — this is a trusted-home-network tool. On
untrusted Wi-Fi, use Tailscale instead and point the app at the `100.x` address or MagicDNS
name. Background: [`docs/adr/0001-lan-listener-for-mobile.md`](../../docs/adr/0001-lan-listener-for-mobile.md).

## Prerequisites

| For             | You need                                                                              |
| --------------- | ------------------------------------------------------------------------------------- |
| Everything      | Node 20+, and AO running on your machine (desktop app, or the daemon from source)     |
| Phone ↔ machine | Same Wi-Fi network, or both on the same Tailnet                                       |
| iOS build       | macOS, Xcode 16+, an Apple ID (a free one gives a 7-day signing profile), a USB cable |
| Android build   | Android Studio (SDK + platform-tools for `adb`), a USB cable                          |

## Install

This package is **not** part of an npm workspace — install from inside it:

```bash
cd packages/mobile
npm install       # .npmrc sets legacy-peer-deps; postinstall runs patch-package
```

Two rules worth knowing before you fight an install:

- **Do not run `npm install --force` here.** It hoists SDK-incompatible transitive Expo deps
  and the app crashes on launch. Plain `npm install` in this directory is correct.
- `metro.config.js` pins `react` and `react-native` to this package's copies. Don't remove
  that — two React instances kill the app at startup with _"main has not been registered"_.

## Step 1 — Turn on Connect Mobile on the desktop

Nothing on the phone works until the desktop opens the LAN bridge. Do this first.

**1. Start the desktop app.** Either launch the packaged AO app, or run it from source
(it starts its own daemon):

```bash
cd frontend && npm run dev      # Electron supervisor + daemon
```

**2. Open the pairing modal:** in the desktop app, **Sidebar → Settings menu → Connect Mobile**.

**3. Flip the "Enable mobile" toggle on.** The bridge binds immediately and the modal reveals
the pairing details:

- a **QR code** (encodes `{v:1, host, port, password}`),
- the plaintext **host:port**, e.g. `192.168.1.84:3011`,
- the 8-character **connection password** (copyable),
- **Regenerate password**, which rotates the secret and drops any connected phone.

Leave the modal open — you scan that QR in step 3. Toggling **off** tears the bridge down,
so the phone goes offline until you turn it back on.

**Headless (daemon only, no Electron UI)?** Start the daemon and drive the same routes over
loopback:

```bash
cd backend && go run .

curl -X POST http://127.0.0.1:3001/api/v1/mobile/enable
curl -s      http://127.0.0.1:3001/api/v1/mobile/status    # → {enabled, host, port, password}
curl -X POST http://127.0.0.1:3001/api/v1/mobile/disable
```

Type the `host`/`port`/`password` from `status` into the app's Settings by hand (step 3).

## Step 2 — Build and install the dev build

You compile the app onto the device once; after that, JS changes hot-reload from Metro and
you only rebuild when native config changes. `ios/` and `android/` are **generated and
gitignored** — the run commands prebuild them for you.

### On a cabled device (the main path)

**iOS, over the cable:**

1. Plug the iPhone in and tap **Trust This Computer**.
2. On the phone: **Settings → Privacy & Security → Developer Mode → On** (the phone reboots).
3. Build and install:

   ```bash
   npx expo run:ios --device      # pick your iPhone from the list
   ```

   If Xcode rejects the bundle identifier or team, open `ios/AO.xcworkspace`, select the
   **AO** target → **Signing & Capabilities**, choose your personal team and let Xcode manage
   signing, then re-run the command.

4. On first launch iOS asks for **Local Network** access. Allow it, or the app cannot reach
   the daemon on your LAN.

**Android, over the cable:**

1. On the phone, enable **Developer options** (tap Build number 7×), then **USB debugging**.
2. Plug in, accept the RSA fingerprint prompt, and confirm the device is visible:

   ```bash
   adb devices      # must list your device as "device", not "unauthorized"
   ```

3. Build and install:

   ```bash
   npx expo run:android --device
   ```

4. Let the phone reach Metro through the cable:

   ```bash
   adb reverse tcp:8081 tcp:8081
   ```

   Optional if the phone is on the same Wi-Fi, but it makes JS reloads immune to flaky Wi-Fi.
   It does **not** cover the daemon connection — that still goes over Wi-Fi (or Tailscale) to
   `host:3011`.

Cleartext HTTP to the bridge works on Android everywhere via `usesCleartextTraffic` in
`app.json`. On iOS, `NSAllowsLocalNetworking` in the prebuilt `Info.plist` only permits
cleartext to link-local, `.local`, and RFC 1918 (LAN) addresses — Tailscale's
`100.64.0.0/10` range is RFC 6598, so iOS blocks plaintext to it. Tailscale pairing on iOS
requires the desktop's secure-pairing mode (TLS via `tailscale serve`).

> **On `expo-dev-client`:** this package doesn't depend on it today, so the debug build
> connects straight to Metro and has no in-app launcher or URL switcher. If you want the
> launcher UI (scan a Metro QR from inside the app, switch bundler URLs), run
> `npx expo install expo-dev-client`, rebuild with `npx expo run:*`, and serve with
> `npx expo start --dev-client`.

### On a simulator / emulator

Same build commands without `--device`. The daemon host differs — a simulator isn't "on your
Wi-Fi" the way a phone is:

```bash
npx expo run:ios         # iOS Simulator    → daemon host 127.0.0.1
npx expo run:android     # Android emulator → daemon host 10.0.2.2
```

Port is `3011` either way. The pairing QR encodes your LAN IP, which usually works here too;
if it doesn't, type the host above by hand (step 3).

## Step 3 — Pair the phone

In the app: **Settings → scan the pairing QR** (grant camera access when asked). One scan
writes host, port, and password, then reconnects — no typing.

Manual entry, if you prefer (or for simulators / Tailscale):

| Field             | Value                                                                                |
| ----------------- | ------------------------------------------------------------------------------------ |
| **Host**          | Your machine's LAN IP (`ipconfig getifaddr en0` on macOS), or Tailscale name/`100.x` |
| **API Port**      | `3011` — the Connect Mobile bridge, **not** the loopback `3001`                      |
| **Password**      | The 8-character password from the Connect Mobile modal                               |
| **Terminal Port** | Legacy, ignored. The daemon serves REST and the `/mux` terminal on the API port      |
| **Use TLS**       | Off for the LAN bridge. On only for real HTTPS, e.g. a Tailscale funnel              |

Tap **Test connection**, then **Save**. The password lives in the device keystore (iOS
Keychain / Android Keystore), never in AsyncStorage.

## Everyday dev loop

With the dev build installed on the device, JS changes need no rebuild — just start Metro
and launch the app from the home screen:

```bash
npm start        # Metro; save a file and the phone hot-reloads
```

Rebuild with `npx expo run:ios|run:android` only when **native** config changes: `app.json`
plugins or permissions, native dependencies, `expo-build-properties`. After dependency
surgery, regenerate the native projects from scratch with `npx expo prebuild --clean`.

## Over-the-air updates

Preview and production builds pull JS-only changes from
[EAS Update](https://docs.expo.dev/eas-update/introduction/), so a fix that touches no
native code reaches phones without a store release. A cold start checks in the
background and applies on the next launch. When the app comes back after 15+ minutes
in the background it applies a downloaded update (a quick reload; sessions live in the
daemon) or checks for one. **Settings → About → App updates** checks on demand,
restarts into a pending update and shows what is running. Dev builds
(`npx expo run:*`) never take updates — they load from Metro.

- **Runtime version** is a
  [fingerprint](https://docs.expo.dev/eas-update/runtime-versions/#fingerprint-runtime-version-policy)
  of the native inputs (dependencies, config plugins, patches; see `fingerprint.config.js`).
  Any native change produces a new runtime, so an update can never land on a build that
  lacks the native code it needs. `npx expo-updates runtimeversion:resolve --platform ios`
  prints the current value.
- **Channels** follow the `eas.json` build profiles: `preview` and `production`.
- **Publishing** is a deliberate local step, like builds. There is no CI publish, so
  no Expo token lives in the repo:

  ```bash
  eas update --channel preview --environment preview -m "fix: ..."
  eas update --channel production --environment production -m "fix: ..." --rollout-percentage 10
  ```

  `--environment` selects the EAS environment variables and is required from SDK 55.
  Start production at a partial rollout and widen it from the EAS dashboard once it looks healthy.
  Publish from the machine that builds, so the fingerprint is computed with the same
  inputs. `eas update:list --branch production` shows what is live (note `--branch`,
  not `--channel`), `eas update:insights` shows adoption, and
  `eas update:roll-back-to-embedded` reverts.

### Build or update?

Don't judge by eye — a diff that touches only `.ts`/`.tsx` can still need a build if it
also touches `app.json`, a plugin, or a dependency. Ask the tool:

```bash
eas fingerprint:compare
```

Matches the live build's fingerprint → ship an update. Doesn't match → it needs a build,
however JS-only the diff looks. `google-services.json`, `eas.json`, `.gitignore` and the
masked-view Android manifest are excluded from the hash (see `fingerprint.config.js`);
everything else that feeds the native build is in it, including `.easignore` — editing
even its comments moves the runtime version.

### Rules that keep this working

- **Never let a fingerprint input differ between machines, or between the build state
  and the publish state.** Every OTA failure this project has hit was one of these, and
  they are all silent: the publish succeeds, the dashboard says live, and no device ever
  matches. Four have been found and closed; assume there is a fifth.
- **Updates published before a build are invisible to that build.** `useEmbeddedUpdate`
  serves the embedded bundle when it is newer than anything on the branch. So after every
  production build, re-publish any JS fixes that still matter — or cut the build from a
  commit that already contains them. Otherwise users on the fresh binary quietly regress
  while the dashboard still shows those updates as live.
- **`roll-back-to-embedded` is per-runtime.** It prompts for one runtime version, and
  runtimes are per-platform. In an incident you must run it **twice**, once for iOS and
  once for Android. Rolling back one platform turns the dashboard green while half your
  users stay broken.
- **Don't change dependencies between a build and the updates published against it.**
  `npx expo install --check` moves the fingerprint. Run it before a build, never after.
  `expo-doctor` reporting patch drift a week after shipping is normal and is not a reason
  to rebuild.
- **Start Metro with `--clear` after any `npm ci` or dependency change**, or you get
  "Unable to resolve module X" for a package that is demonstrably installed.
- **`eas.json` is `skip-worktree`** on the publishing machine so local App Store Connect
  submit credentials stay out of the public repo. EAS Build reads the committed file, so
  any local edit to it — a new profile, a channel change — never reaches a build until
  the bit is cleared and the change is committed.
- **`google-services.json` reaches EAS Build as the `GOOGLE_SERVICES_JSON` file
  environment variable**, not by being committed. Never go back to a temp commit: that
  required deleting the file's line from `.gitignore`, which is itself a fingerprint
  input, and it put a public-repo leak one `git push` away.

### Verified, and not

Checked on device against real builds (Android `307dee3c…`, iOS `66d18d1d…`): build and
publishing-machine fingerprints matching, the Settings check-and-restart path, cold start
applying on the second launch, and rollback to embedded over populated storage without a
crash.

**The 15-minute resume path is covered by unit tests only.** `onForeground` is tested
directly; the `AppState` wiring around it has never been exercised on a device. Note also
that any foreground resets the timer, so testing it needs one uninterrupted background
stretch.

## Store updates

OTA covers JS only. A native change mints a new fingerprint runtime, so a build
in the field stops being offered updates the moment a newer binary ships — silently.
This is the other half: when a newer **native binary** is live on a store, the app
says so once per launch and takes the user there. **Settings → About → App Store /
Play Store** checks on demand.

- **Android** uses [Play In-App Updates](https://developer.android.com/guide/playcore/in-app-updates)
  via `expo-in-app-updates`. Play compares the installed `versionCode` itself, so the
  app never reads or configures a version. Only works for builds installed from Play;
  sideloaded and dev builds fail the check silently.
- **iOS** queries the iTunes Search API by bundle id in plain JS. The response carries
  the App Store id, so nothing has to be configured. It is inert until the app is
  actually on the App Store.
- `expo-in-app-updates` is therefore **excluded from Apple autolinking**
  (`package.json` → `expo.autolinking.apple.exclude`) — it is Android-only code here.
  Keep it that way: linking the unused pod puts the package into the iOS fingerprint,
  which would change the iOS runtime version on every dependency bump and force a
  native release for changes that are pure JS. `lib/inAppUpdates.ts` binds the native
  module with `requireOptionalNativeModule`, which answers `null` on iOS instead of
  throwing the way a plain import would.
- **Two tiers.** The nudge is a dismissible sheet, shown at most once a day and three
  times per store version (a swipe counts as a dismissal). The insistent tier is Play's
  own fullscreen updater — note it is still cancellable: `startUpdateFlowForResult`
  resolves as soon as the dialog opens, and cancelling leaves the app running. It is
  entered only when Play asks for it — publish the release with `inAppUpdatePriority`
  4 or 5 in `Edits.tracks.releases` through the Play Developer API. Priority can only
  be set while rolling a release out and **can never be changed afterwards**. Raise it
  only once the new build is *live* on the store, not merely approved. iOS has no
  equivalent channel and Apple discourages blocking, so iOS is nudge-only.
- **Version floor.** Two EAS environment variables let a release be declared
  without shipping app code: `EXPO_PUBLIC_AO_MIN_APP_VERSION` (below it, the
  update stops being optional) and `EXPO_PUBLIC_AO_LATEST_APP_VERSION` (below it,
  the usual once-a-day nudge). Both unset means the floor is inert, which is how
  it ships. Move one and publish:

  ```bash
  eas env:create --environment production --name EXPO_PUBLIC_AO_MIN_APP_VERSION --value 1.3.0 --visibility plaintext
  eas update --channel production --environment production -m "raise the floor to 1.3.0" --rollout-percentage 10
  ```

  Four things to know before you do:
  - **`min` can only escalate an update the store already confirmed exists.** It
    can raise a confirmed update from dismissible to blocking; it can never invent
    one. So a mistyped floor cannot strand anyone, and iOS stays nudge-only until
    the App Store listing exists — then starts blocking on its own, no code change.
  - **Visibility must be `plaintext`.** Secret variables are not readable outside
    EAS servers, so they do not resolve during `eas update` and the floor silently
    falls back to inert.
  - **One publish per runtime you want to reach.** Values are inlined into the JS
    bundle. A `version` stamp changes the fingerprint, so after one, `main` only
    reaches the new runtime — publish from the older release's tag to reach that
    cohort. The EAS values are read at publish time, so no code edit on that tag.
  - **It only distinguishes versions you actually stamp.** `eas.json`
    `autoIncrement` moves the build number, not `version`.
- **Flexible updates are never started, deliberately.** `expo-in-app-updates` calls
  `completeUpdate()` as soon as a flexible download finishes, which restarts the app
  unannounced; Google's own contract is that a flexible update restarts only when the
  user chooses to. A surprise restart is wrong for a remote control over long-running
  agents, so the dismissible sheet is the soft tier and opting in goes straight to the
  immediate flow.
- Everything fails open: an unreachable store, an unpublished bundle id or a non-Play
  install all read as "no update" rather than an error.

## Troubleshooting

| Symptom                                           | Fix                                                                                                                                                          |
| ------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| Crash at launch, _"main has not been registered"_ | Two React copies. Keep `metro.config.js` intact, reinstall with plain `npm install` (never `--force`), then `npx expo prebuild --clean`.                     |
| **Test connection** fails, everything times out   | Connect Mobile is off on the desktop, wrong port (`3011`, not `3001`), phone on a different network, or the macOS firewall is blocking incoming connections. |
| 401 / invalid password                            | The password was regenerated on the desktop. Re-scan the QR.                                                                                                 |
| Locked out after repeated failures                | The bridge locks out a source after 5 failed attempts. Wait it out, or toggle Connect Mobile off and on.                                                     |
| `adb devices` shows `unauthorized`                | Re-accept the USB debugging prompt on the phone.                                                                                                             |
| iOS app installs, then closes immediately         | Untrusted developer profile: **Settings → General → VPN & Device Management → trust your Apple ID**.                                                         |
| App runs but can't reach the daemon (iOS)         | The Local Network prompt was denied: **Settings → Privacy & Security → Local Network → AO** → on.                                                            |
| Phone can't reach Metro                           | `adb reverse tcp:8081 tcp:8081` (Android), or `npx expo start --tunnel` (either platform).                                                                   |
| An update never arrives                           | Its runtime version differs from the build's — a native change since that build. Compare `runtimeversion:resolve` with the runtime shown in a bug-report body. |
| Terminal renders blank                            | The xterm WebView is patched via `patch-package`; confirm `postinstall` ran (`npx patch-package`).                                                           |

## Project layout

```
app/                 expo-router routes
  (tabs)/            Kanban (index), PRs, Orchestrator, Settings
  session/[id].tsx   persisted-mode router (native Chat or Terminal UI)
  shell/[handleId]   session-scoped worktree shell over the existing mux
  preview/[id]       authenticated session preview browser
  spawn.tsx          spawn flow
  pair.tsx           pairing-QR scanner
lib/
  api.ts             REST client for the daemon API
  chat/              paged/SSE conversation client and native Chat UI
  session/           existing TUI/xterm surface and terminal controls
  mux.ts             /mux WebSocket terminal transport
  config.ts          server config — password in SecureStore, the rest in AsyncStorage
  pairing.ts         pairing-QR payload parser
  store.tsx          app state + connection polling
  theme.ts, ui.tsx   design primitives
scripts/             ao-phone-proxy.js — superseded by Connect Mobile, kept for reference
```

## Verify

```bash
npm run typecheck    # tsc --noEmit
npm test             # pure state/API/parser regression suite
npx expo export --platform ios
npx expo export --platform android
```
