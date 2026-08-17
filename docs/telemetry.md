# Product telemetry

AO collects limited product-usage and reliability data to learn which parts of
the app are useful and whether releases are working as expected. The data is
not account-linked: AO does not attach a name or email address. It is
pseudonymous rather than unlinkable, because a random installation identifier
lets events from the same installation be counted together over time.

Remote telemetry is enabled in production desktop and mobile releases. A
packaged desktop release also enables telemetry for the daemon it starts.
Development builds and daemons started directly do not send remote telemetry by
default.

## What AO sends

AO sends structured events in a few broad categories:

- App usage, such as launching AO, viewing a coarse area of the interface, or
  starting a task or agent session
- Feature outcomes, such as whether creating a project, starting an agent,
  connecting the mobile app, or installing an update succeeded
- Reliability data, such as an error type and context, a crash message and
  stack trace after path redaction, an HTTP status, or an agent waiting for
  input
- Basic environment information, such as the AO version, operating system,
  release channel, and which supported agent types are available
- A random installation identifier and one-way hashes of project or session
  identifiers when an event needs them
- Coarse mobile-app usage, such as pairing, reconnecting, completing onboarding,
  opening a notification, or using a core action

AO uses [PostHog](https://posthog.com/privacy) to process remote product
telemetry. PostHog receives standard connection and device metadata, including
the connection's IP address, device type, and operating system, and may use it
to derive approximate geographic information.

The installation identifier lets PostHog group activity from one AO
installation over time. Hashed project and session identifiers can likewise
group events for the same project or session without sending those identifiers
in plain text. Neither is linked to an AO account.

## What AO does not intentionally send

Product telemetry is designed not to include:

- Source code, diffs, commits, or file contents
- Prompts, agent conversations, agent output, or terminal contents
- Shell command arguments, command history, or environment variables
- Repository names, project names, branch names, or plain-text file paths
- API keys, access tokens, passwords, or other credentials
- Names, email addresses, or account identities

The optional website waitlist is separate from product telemetry. If you submit
an email address there, it is used to manage that waitlist as described in the
[privacy policy](https://aoagents.dev/privacy).

## How AO limits the data

- AO generates a random installation identifier on first run. It is stored at
  `~/.ao/data/telemetry_install_id` (or under `AO_DATA_DIR`) and is not linked to
  a personal account.
- Project and session identifiers included in telemetry are one-way hashed.
  Hashing hides the plain text but still allows related events to be grouped.
- Absolute local paths and local application URLs detected in desktop events
  are replaced with redaction markers before the events are sent.
- Daemon events sent to PostHog and mobile events accept a fixed set of
  properties; unexpected fields are discarded.
- Event rates are limited to reduce repeated background activity and error
  loops.
- Person profiles and session recording are disabled in the desktop and mobile
  apps. AO does not automatically record screens, clicks, or touches.

Separately from remote telemetry, the daemon can keep a local copy of
operational events in AO's SQLite database. While local telemetry is active, AO
periodically prunes records older than 30 days. This data stays under `~/.ao` on
your machine.

## Turn desktop and daemon telemetry off

AO currently provides environment-variable controls rather than an in-app
desktop setting. Set all three variables in the environment used to launch AO:

```bash
export AO_TELEMETRY_RENDERER=off
export AO_TELEMETRY_EVENTS=off
export AO_TELEMETRY_REMOTE=off
```

Then restart AO. `AO_TELEMETRY_RENDERER=off` disables events sent directly by
the desktop interface. `AO_TELEMETRY_EVENTS=off` disables daemon event capture,
including its local copy. `AO_TELEMETRY_REMOTE=off` explicitly disables daemon
export to PostHog.

The values must reach the desktop app process itself. For example, variables in
a shell startup file may not be inherited when you launch AO from the macOS
Finder or Dock.

If you run the daemon without the desktop app, event capture and remote export
are already off unless you enable them.

These environment variables do not control the mobile app. The current
production mobile app does not provide an in-app telemetry opt-out. Turning
desktop or daemon telemetry off stops new collection there; it does not delete
events already sent to PostHog, remove the local installation identifier, or
delete existing local telemetry records. Automatic deletion of local records
older than 30 days resumes if daemon event capture is enabled again.

## Questions or corrections

For the broader data policy, retention information, and contact options, see
the [AO privacy policy](https://aoagents.dev/privacy). You can report a problem
with this documentation in the
[GitHub repository](https://github.com/Untrivial-ai/agent-orchestrator).
