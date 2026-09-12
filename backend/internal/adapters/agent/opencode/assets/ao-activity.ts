// agent-orchestrator: managed opencode activity plugin (do not edit)
//
// It maps opencode's native lifecycle events onto AO's normalized
// activity events:
//   session.created                       -> `ao hooks opencode session-start`
//   message.updated / message.part.updated -> `ao hooks opencode user-prompt-submit`
//   tool.execute.before / tool.execute.after -> `ao hooks opencode active`
//   session.status (status.type == idle)   -> `ao hooks opencode stop`
//   permission.asked / question.asked      -> `ao hooks opencode permission-blocked`
//   permission/question reply or rejection -> `ao hooks opencode active`
//
// The opencode-native session id (and prompt/model where known) is piped to the
// hook command as JSON on stdin, run with cwd set to the worktree so AO can
// correlate the opencode session to its AO session. Every invocation is
// best-effort and must never crash the user's opencode session: a missing `ao`
// binary is a guarded no-op (`command -v ao`), and spawn exceptions, non-zero
// exit codes, and malformed event payloads are caught and surfaced through
// opencode's structured logger (client.app.log) for diagnosis — never rethrown.
//
// `import type` is erased at runtime by Bun's transpiler, so this loads even
// before opencode has installed @opencode-ai/plugin into the config dir.
import type { Plugin } from "@opencode-ai/plugin"

export const aoActivity: Plugin = async ({ directory, client }) => {
  // ao hooks must never be able to hang opencode: cap each invocation, matching
  // the 30s timeout the claude-code and codex hook entries use.
  const HOOK_TIMEOUT_MS = 30_000
  // A user message is reported at most twice (see reportUserPrompt): an optional
  // early empty report, then an upgrade carrying the prompt text. Maps a message
  // id to whether the report we already sent included the prompt text.
  const promptReports = new Map<string, boolean>()
  // message.* events don't carry the session id, so track it from events that do.
  let currentSessionID: string | null = null
  // The model of the most recent assistant message, forwarded for context.
  let currentModel: string | null = null
  // AO fences each provider generation with AO_RUNTIME_LAUNCH_ID; carry it in
  // the payload so hooks work even when child-process env inheritance is
  // trimmed by the host runtime.
  const launchID: string = (process.env.AO_RUNTIME_LAUNCH_ID ?? "").trim()
  const messageStore = new Map<string, any>()

  // Wrap in `sh -c` with a guard so a missing `ao` binary is a silent no-op
  // (exit 0) rather than a per-event error in the user's session.
  function hookCmd(hookName: string): string[] {
    return ["sh", "-c", `if ! command -v ao >/dev/null 2>&1; then exit 0; fi; exec ao hooks opencode ${hookName}`]
  }

  // Report a hook failure through opencode's structured logger. Best-effort: the
  // log call must itself never throw or reject back into opencode, hence the
  // optional chaining + swallowed rejection.
  function logHookFailure(hookName: string, detail: string) {
    try {
      void client?.app
        ?.log?.({ body: { service: "ao-activity", level: "error", message: `hook ${hookName} failed: ${detail}` } })
        ?.catch?.(() => {})
    } catch {
      // The logger itself is unavailable — nothing more we can safely do.
    }
  }

  // All hooks are dispatched synchronously (Bun.spawnSync), for two reasons:
  //   1. Ordering. An async hook yields the event loop; if opencode does not
  //      await the handler's promise, a later event (e.g. message.updated ->
  //      user-prompt-submit) could complete before an in-flight async
  //      session-start, so AO would see the prompt before the session is
  //      registered. spawnSync blocks opencode's single-threaded loop until the
  //      hook returns, so events are reported strictly in dispatch order.
  //   2. `opencode run` exits on the idle event, so an async stop hook would be
  //      killed before completing.
  //
  // A non-zero exit (the guard makes a missing `ao` exit 0, so this is a real
  // `ao hooks` failure) or a spawn exception is logged with its stderr and never
  // rethrown, so reporting failures are diagnosable without crashing opencode.
  function callHookSync(hookName: string, payload: Record<string, unknown>) {
    try {
      const result = Bun.spawnSync(hookCmd(hookName), {
        cwd: directory,
        env: { ...process.env, AO_RUNTIME_LAUNCH_ID: launchID },
        stdin: new TextEncoder().encode(JSON.stringify({ ...payload, launch_id: launchID }) + "\n"),
        stdout: "ignore",
        stderr: "pipe",
        timeout: HOOK_TIMEOUT_MS,
      })
      if (!result.success) {
        const stderr = result.stderr ? new TextDecoder().decode(result.stderr).trim() : ""
        logHookFailure(hookName, `exited ${result.exitCode}${stderr ? `: ${stderr}` : ""}`)
      }
    } catch (err) {
      // The spawn itself failed (e.g. no `sh` on PATH). Never propagate.
      logHookFailure(hookName, err instanceof Error ? err.message : String(err))
    }
  }

  function switchedSession(sessionID: string): boolean {
    if (currentSessionID === sessionID) return false
    promptReports.clear()
    messageStore.clear()
    currentModel = null
    currentSessionID = sessionID
    return true
  }

  // Report a user prompt, preferring the one that carries the prompt text.
  // message.updated can arrive before message.part.updated with no text, so an
  // early empty report must NOT dedup away the later text report — otherwise the
  // prompt never reaches AO and title-from-prompt metadata breaks. Therefore: an
  // empty report fires at most once (so run-mode flows that omit the text part
  // still mark the session active), and a text report fires once and is terminal.
  function reportUserPrompt(sessionID: string, messageID: string, prompt: string) {
    const hasText = prompt.length > 0
    const reportedWithText = promptReports.get(messageID)
    if (reportedWithText) return // already reported with text — terminal
    if (reportedWithText === false && !hasText) return // already reported empty; no new info
    promptReports.set(messageID, hasText)
    callHookSync("user-prompt-submit", { session_id: sessionID, prompt, model: currentModel ?? "" })
  }

  return {
    event: async ({ event }) => {
      try {
        switch (event.type) {
          case "session.created": {
            const session = (event as any).properties?.info
            if (!session?.id) break
            if (switchedSession(session.id)) {
              callHookSync("session-start", { session_id: session.id })
            }
            break
          }

          case "message.updated": {
            const msg = (event as any).properties?.info
            if (!msg) break
            if (msg.sessionID && switchedSession(msg.sessionID)) {
              callHookSync("session-start", { session_id: msg.sessionID })
            }
            if (msg.role === "assistant" && msg.modelID) currentModel = msg.modelID
            // Fallback: some `opencode run` flows never deliver message.part.updated
            // for the prompt, so start the turn from the user message itself.
            if (msg.role === "user") {
              messageStore.set(msg.id, msg)
              const sessionID = msg.sessionID ?? currentSessionID
              if (sessionID) reportUserPrompt(sessionID, msg.id, "")
            }
            break
          }

          case "message.part.updated": {
            const part = (event as any).properties?.part
            if (!part?.messageID) break
            const msg = messageStore.get(part.messageID)
            if (msg?.role === "user" && part.type === "text") {
              const sessionID = msg.sessionID ?? currentSessionID
              const prompt = part.text ?? ""
              if (sessionID) reportUserPrompt(sessionID, msg.id, prompt)
              if (prompt.length > 0) messageStore.delete(part.messageID)
            }
            break
          }

          case "session.status": {
            // session.status fires in both TUI and `opencode run`; session.idle
            // is deprecated and not reliably emitted in run mode.
            // AO's "stop" hook means "the current turn is idle/finished", not
            // "the whole native session has terminated", so multi-turn TUI
            // sessions intentionally emit one stop per idle transition.
            const props = (event as any).properties
            if (props?.status?.type !== "idle") break
            const sessionID = props?.sessionID ?? currentSessionID
            if (!sessionID) break
            callHookSync("stop", { session_id: sessionID, model: currentModel ?? "" })
            break
          }

          case "permission.asked":
          case "question.asked": {
            const sessionID = (event as any).properties?.sessionID ?? currentSessionID
            if (!sessionID) break
            callHookSync("permission-blocked", { session_id: sessionID, model: currentModel ?? "" })
            break
          }

          case "permission.replied":
          case "question.replied":
          case "question.rejected": {
            const sessionID = (event as any).properties?.sessionID ?? currentSessionID
            if (!sessionID) break
            callHookSync("active", { session_id: sessionID, model: currentModel ?? "" })
            break
          }

        }
      } catch (err) {
        // A malformed/unexpected event payload must never crash opencode; log
        // it (tagged with the event type) for diagnosis and move on.
        logHookFailure(`event:${(event as any)?.type ?? "unknown"}`, err instanceof Error ? err.message : String(err))
      }
    },
    "tool.execute.before": async (input) => {
      callHookSync("active", { session_id: input.sessionID, model: currentModel ?? "" })
    },
    "tool.execute.after": async (input) => {
      // Tool completion is still activity; session.status(idle) owns the
      // transition back to idle after the turn finishes.
      callHookSync("active", { session_id: input.sessionID, model: currentModel ?? "" })
    },
  }
}
