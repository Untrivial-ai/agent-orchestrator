# One-Click Vendor Installer Design

## Goal

Extend PR #4221 so every harness with a native installer for the current
operating system can be installed with one click from Harness Settings.
Package-manager recipes remain preferred, while first-party remote scripts
become automatic fallback methods instead of instruction-only plans.

The user explicitly accepts the supply-chain risk of running mutable vendor
scripts and does not want an additional confirmation dialog. Clicking
**Install** or **Reinstall** is the authorization to run the selected,
daemon-owned recipe.

## Scope

- Keep the existing one-harness-at-a-time Install and Reinstall actions.
- Enable first-party shell installers on macOS and Linux and first-party
  PowerShell installers on native Windows.
- Retain package-manager methods and their current preference order when they
  are viable.
- Use a vendor script as the recommended fallback when no safer automatic
  method is viable.
- Preserve instruction-only behavior where the vendor has no native installer
  for the current OS. In particular, WSL-only Windows flows remain manual.
- Keep authentication separate. Installation success does not imply that the
  harness is logged in.

This design does not install all missing harnesses automatically at startup and
does not add a bulk "install everything" operation.

## User Experience

Harness Settings continues to show one row per supported harness.

- If the harness is absent and at least one method is viable, the row shows
  **Install**. One click starts the selected method without a second prompt.
- When multiple methods are available, AO recommends the first viable
  package-manager method. The official vendor installer remains selectable and
  becomes the fallback when package-manager preflight fails.
- Active jobs keep the existing spinner, durable progress, diagnostics, and
  recovery behavior.
- Successful jobs transition through adapter-backed verification before the
  row is reported as installed.
- Unsupported native platforms continue to show **Instructions**, with the
  reason naming the supported alternative such as WSL.

The UI never receives authority to construct a URL, shell command, or
PowerShell expression. It submits only a harness ID and stable method ID.

## Trust Model

Remote scripts are mutable and cannot be proven equivalent to code reviewed
when AO was released. This is an intentional product trade-off in favor of
one-click installation. AO limits the resulting authority but does not claim
to eliminate vendor supply-chain risk.

Only exact first-party URLs registered in Go source are eligible. A renderer
request cannot override the URL, interpreter, arguments, environment, download
limit, or execution timeout. Recipes must use HTTPS. Redirects may be followed
only for a bounded number of hops and every hop must remain HTTPS.

AO must not use `curl | shell`, `irm | iex`, `shell -c`, or another streaming
execution pipeline. It downloads the complete response first, validates the
transport and size constraints, records a SHA-256 digest for diagnostics, and
then executes the saved file.

## Architecture

### Server-owned recipes

`systeminstall.Plan` represents either a package-manager argv recipe or a
remote-script recipe. A remote-script recipe contains:

- stable method ID `official-installer`;
- exact HTTPS URL;
- platform interpreter (`sh`, `bash`, or PowerShell);
- fixed interpreter arguments;
- display-safe command text and first-party documentation URL.

The recipe registry remains in
`backend/internal/service/systeminstall/agentplans.go`. Script URLs are never
accepted over HTTP.

The method lists add official installers for script-publishing harnesses. Safe
package managers keep priority: Homebrew/winget, npm, `uv`/pipx, or Bun remain
recommended when viable. The script becomes the next viable fallback.

### Download and execution boundary

The system execution adapter gains a dedicated remote-script operation instead
of teaching the service to assemble shell pipelines. The operation:

1. applies a bounded download context;
2. performs an HTTPS GET with a bounded redirect count;
3. rejects non-success HTTP responses, non-HTTPS redirects, and responses over
   the configured maximum size;
4. writes the script with mode `0600` inside an AO-owned per-job directory
   beneath `<AO_DATA_DIR>/installers/tmp`;
5. invokes the fixed interpreter with the script path, closed stdin, the
   existing noninteractive environment, and the existing install timeout;
6. streams bounded stdout/stderr into the durable job diagnostics;
7. removes the per-job directory after execution on both success and failure.

On Windows, AO selects an already-installed PowerShell executable and runs the
temporary `.ps1` file with `-NoProfile`, `-NonInteractive`, and
`-ExecutionPolicy Bypass`. On Unix, it invokes the recipe's fixed `sh` or
`bash` executable with the temporary file path.

The adapter records the source URL and SHA-256 digest in diagnostics but never
stores or logs the downloaded script body.

### Durable lifecycle and verification

The existing durable lifecycle remains:

`installing -> verifying -> succeeded | failed`

Interrupted jobs are not automatically rerun. After execution, the canonical
agent adapter resolves the exact binary and runs the existing bounded,
non-authenticating version probe. A zero exit from the installer is not enough
to mark the job successful.

Concurrent installation of the same harness remains rejected. Different
harnesses may install concurrently. The existing Droid session gate remains in
force for both package-manager and vendor-script methods.

## Platform Coverage

The implementation enables official script methods wherever the repository has
a verified first-party native URL. That includes the current script-only set:

- Cursor, Aider, Grok, Kimi, Kiro, and AGY on Unix and native Windows;
- Goose, Devin, Muse, and Prime Agent on macOS/Linux.

It also exposes official scripts as fallbacks for harnesses that already prefer
package managers when the vendor publishes such a native installer, including
Claude Code, Codex, Pi, Amp, Droid, Qwen, Autohand, OpenCode, and OMP where
supported by first-party documentation.

Windows remains instruction-only for harnesses whose vendor documents only WSL
or no native CLI, currently Goose, Devin, Muse, and Prime Agent. AO does not
silently install into WSL because that would target a different runtime and
PATH than the native desktop daemon verifies.

## Failure Handling

Failures remain visible in the existing expandable diagnostics UI. Messages
distinguish:

- downloader unavailable or unsupported interpreter;
- TLS, redirect, HTTP status, or download-size failure;
- temporary-file creation or cleanup failure;
- installer timeout, cancellation, or non-zero exit;
- installer completed but adapter resolution/version verification failed.

Cleanup errors are recorded without replacing a more important install or
verification failure. No failure triggers an automatic retry.

## Testing

Backend tests must cover:

- every script recipe's OS availability, exact URL, interpreter, and method ID;
- package-manager-first recommendation and script fallback selection;
- renderer-controlled URLs and commands remaining impossible;
- HTTPS-only requests and HTTPS-only redirects;
- bounded redirects, response size, download time, and execution time;
- closed stdin and noninteractive environment;
- AO-owned `0700` job directory and `0600` script file permissions on Unix;
- temporary-file cleanup after success, failure, timeout, and cancellation;
- PowerShell argv on Windows and `sh`/`bash` argv on Unix;
- durable state transitions, diagnostics digest, interruption behavior, Droid
  gating, and adapter-backed verification.

Controller and API tests must prove that the client still submits only target
and method IDs. Frontend tests must prove that previously manual script-only
harnesses show one-click Install, native-unsupported platforms still show
Instructions, and existing progress/recovery/diagnostic behavior is unchanged.

Final verification includes the full Go suite, frontend tests and typecheck,
generated API drift checks if contracts change, and a real Electron Harness
Settings installation exercise against isolated AO data. Live testing must use
a harmless fixture server or a reviewed test script; it must not install or
reinstall real third-party harnesses without a separate explicit test request.

## Documentation Update

The earlier safe-installer design and PR description must be updated to remove
the claim that mutable vendor scripts are never executed automatically. They
must instead state the one-click trust decision and the download-to-file
controls described here.
