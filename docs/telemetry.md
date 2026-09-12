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
- The GitHub organization or account that owns a project's configured remote,
  recorded on project-add events. Only the owner segment is sent, never the
  repository name, path, or URL. For a personal repository this owner is the
  user's own GitHub username, so this particular value is not anonymous. We use
  it to understand which organizations and developers get the most value from
  AO, so we can prioritize improvements and reach out for feedback
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

The GitHub owner segment described under "What AO sends" is the one
GitHub-derived value AO does send. It is limited to the owning
organization or account and never includes the repository, path, or URL.

The optional website waitlist is separate from product telemetry. If you submit
an email address there, it is used to manage that waitlist as described in the
[privacy policy](https://useao.dev/privacy).

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

## Agent-switch failure reporting (staged, production disabled)

AO contains a separate, consent-gated reliability path for asynchronous
agent-switch failures. It is intentionally failure-only: a successful switch,
an expected validation rejection, an idempotent replay, a stale callback, or a
transient condition that is proven recovered creates no failure receipt, no
outbox payload, and no Sentry event.

When enabled in a future release, an eligible failure event is limited to a
closed set of operational fields: event ID and occurrence time; bounded title,
level, environment, platform, operating system, release, and channel; report
kind, subsystem, classifier callsite, durable phase, failure point, broad
error/fault code, execution and session mode, source and target harness,
target-start mode, runtime backend, call outcome, ownership, compensation, user
impact, elapsed-time bucket, and tri-state source-stop, target-owner, and
recovery-gate facts. The local outbox schema/envelope versions are not exported
as event fields. Eligible semantic and process events may attach a bounded,
sanitized stack containing repository-relative filenames, line numbers,
packages, and function names; panic events require those sanitized frames but
never include the panic value. The event does not contain prompts, conversation
content, terminal output, commands, provider payloads, repository or branch
names, local paths, runtime handles, native identities, switch/session/project
identifiers, raw errors, or panic values. Local identifiers used to decide
whether a frontend failure is still current are stripped before event
construction.

Eligible daemon events are stored in a dedicated local delivery outbox before
network delivery. Every pending, leased, delivered, or discarded payload row
has a hard seven-day expiry. An already-consented payload is intentionally
independent of the switch foreign key, so deleting the switch or session does
not delete that payload before its TTL. Separate payload-free receipts prevent
the same incident from being enrolled again. Terminal/run receipts remain for
seven days; an unresolved receipt may remain while its switch remains
unresolved and, after resolution, is retained for seven more days. Receipts
contain only the minimum local deduplication facts and are never sent remotely.

Opting in does not backfill old terminal switch history. It may enroll the
current state of an unresolved recovery marker as a new incident at opt-in time,
so AO can report a problem that is still affecting the user without exporting a
pre-consent occurrence timestamp. Delivery is at-least-once: if the provider
accepts an event but its response is lost, AO may retry the same event ID. This
can produce more than one provider occurrence, while the stable fingerprint
groups the occurrences into one issue.

The required opt-out sequence is: Electron main closes and drains its sender;
the daemon closes and drains its delivery gate; main durably writes the disabled
consent generation; the daemon rereads that generation, mirrors it, and purges
every pending, leased, delivered, or discarded outbox payload; main purges its
local transport cache and renderer queue; only then may the UI acknowledge the
change. If the daemon is unavailable or any cleanup cannot be proven, the UI
reports `cleanup_pending` rather than claiming completion. Payload-free receipts
remain solely to prevent a later duplicate enrollment. A provider may already
have accepted a request that was in flight before cancellation; AO cannot recall
data already received by the provider.

This path uses Sentry as the intended processor. Like any remote endpoint,
Sentry receives connection metadata such as the source IP address even though
AO does not place an IP address in the event body. The Sentry organization's IP
storage/scrubbing setting, data residency, retention, and automatic-context
settings have not yet received the required dated privacy approval. The
production feature flag therefore remains disabled and AO does not initialize
this agent-switch Sentry sender in production. Windows fails closed: event
consent is treated as disabled and an enable acknowledgement is rejected until
a tested native write-through replacement satisfies the policy-file durability
contract.

## Turn desktop and daemon telemetry off

The desktop General Settings page includes an **Event reporting** control for
the staged agent-switch reliability path. The environment-variable controls
below continue to govern the existing desktop/daemon product-telemetry paths.
Set all three variables in the environment used to launch AO:

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
desktop or daemon product telemetry off stops new collection there; it does not
delete events already sent to PostHog, remove the local installation identifier,
or delete existing local product-telemetry records. Automatic deletion of those
local records older than 30 days resumes if daemon event capture is enabled
again. The separate Event reporting opt-out follows the purge sequence above:
it deletes agent-switch outbox payloads but retains the durable switch/failure
state required for product recovery and payload-free deduplication receipts.

## Questions or corrections

For the broader data policy, retention information, and contact options, see
the [AO privacy policy](https://useao.dev/privacy). You can report a problem
with this documentation in the
[GitHub repository](https://github.com/Untrivial-ai/agent-orchestrator).
