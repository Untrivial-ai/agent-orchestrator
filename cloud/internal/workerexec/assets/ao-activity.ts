// agent-orchestrator: managed opencode activity plugin (do not edit)
//
// OpenCode v2 (@opencode/cli) rewrote the plugin system. This plugin is loaded
// from the auto-scanned `.opencode/plugin/` directory (unchanged from v1) and is
// a plain default-exported object `{ id, setup(ctx) }` — NO runtime import, so it
// loads without @opencode/plugin being resolvable in the sandbox (a v2 `import
// { define }` would fail with "Cannot find package '@opencode/plugin'"). Types
// are intentionally omitted for the same reason.
//
// It maps opencode v2's native lifecycle onto AO's normalized activity events
// (verified live against opencode 2.0.19 + mimo-v2.6-flash-free):
//   first event for a new session / session.created  -> `ao hooks opencode session-start`
//   session.inbox.enqueued (item.type == "user")     -> `ao hooks opencode user-prompt-submit`
//   session.execution.started / session.tool.called,
//     tool execute.before/after, permission.replied  -> `ao hooks opencode active`
//   session.execution.{succeeded,failed,interrupted} -> `ao hooks opencode stop`
//   permission.asked                                 -> `ao hooks opencode permission-blocked`
//
// v2 event payloads live under `event.data` (v1 used `event.properties`), with the
// opencode session id at `event.data.sessionID`. Every invocation is best-effort
// and must never crash opencode: a missing `ao` binary is a guarded no-op and all
// spawn/stream failures are swallowed. The opencode session id (+ prompt/model
// where known) is piped as JSON on stdin, cwd = the worktree, so AO can correlate
// the opencode session to its AO session.
export default {
  id: "ao-activity",
  setup: (ctx: any) => {
    const directory: string = ctx?.location?.directory ?? process.cwd()
    // AO fences each provider generation with AO_RUNTIME_LAUNCH_ID; carry it so
    // hooks work even when child-process env inheritance is trimmed.
    const launchID: string = (process.env.AO_RUNTIME_LAUNCH_ID ?? "").trim()
    const HOOK_TIMEOUT_MS = 30_000
    // Per-message dedup for the prompt report (see reportUserPrompt).
    const promptReports = new Map<string, boolean>()
    let currentSessionID: string | null = null
    let currentModel: string | null = null
    const ao = Bun.which("ao")

    // Synchronous dispatch preserves event ordering (opencode's loop blocks until
    // the hook returns) and survives `opencode run` exiting on the stop event. A
    // missing `ao` is a silent no-op; spawn failures are swallowed.
    function callHookSync(hookName: string, payload: Record<string, unknown>) {
      if (!ao) return
      try {
        Bun.spawnSync([ao, "hooks", "opencode", hookName], {
          cwd: directory,
          env: { ...process.env, AO_RUNTIME_LAUNCH_ID: launchID },
          stdin: new TextEncoder().encode(JSON.stringify({ ...payload, launch_id: launchID }) + "\n"),
          stdout: "ignore",
          stderr: "ignore",
          timeout: HOOK_TIMEOUT_MS,
        })
      } catch {
        // never propagate into opencode
      }
    }

    // Emit session-start the first time we see a session id. session.created can
    // fire before the plugin's event subscription is active in `run` mode, so we
    // never rely on it alone — the first event carrying a new sessionID starts
    // the session and resets per-session state (mirrors v1's switchedSession).
    function ensureSession(sessionID: string | undefined | null): boolean {
      if (!sessionID || sessionID === currentSessionID) return false
      currentSessionID = sessionID
      promptReports.clear()
      currentModel = null
      callHookSync("session-start", { session_id: sessionID })
      return true
    }

    function reportUserPrompt(sessionID: string, key: string, prompt: string) {
      const hasText = prompt.length > 0
      const reported = promptReports.get(key)
      if (reported) return
      if (reported === false && !hasText) return
      promptReports.set(key, hasText)
      callHookSync("user-prompt-submit", { session_id: sessionID, prompt, model: currentModel ?? "" })
    }

    void (async () => {
      try {
        for await (const event of ctx.event.subscribe()) {
          try {
            const type: string = event?.type ?? ""
            const d: any = event?.data ?? {}
            const sessionID: string | undefined = d.sessionID
            switch (type) {
              case "session.created": {
                if (d.model?.id) {
                  currentModel = d.model.providerID ? `${d.model.providerID}/${d.model.id}` : d.model.id
                }
                ensureSession(sessionID)
                break
              }
              case "session.model.selected": {
                const m = d.model
                if (m?.id) currentModel = m.providerID ? `${m.providerID}/${m.id}` : m.id
                break
              }
              case "session.inbox.enqueued": {
                if (!sessionID) break
                ensureSession(sessionID)
                if (d.item?.type === "user") {
                  const text: string = d.item?.payload?.text ?? ""
                  reportUserPrompt(sessionID, d.inboxID ?? sessionID, text)
                }
                break
              }
              case "session.execution.started":
              case "session.tool.called": {
                if (!sessionID) break
                ensureSession(sessionID)
                callHookSync("active", { session_id: sessionID, model: currentModel ?? "" })
                break
              }
              case "session.execution.succeeded":
              case "session.execution.failed":
              case "session.execution.interrupted": {
                if (!sessionID) break
                ensureSession(sessionID)
                callHookSync("stop", { session_id: sessionID, model: currentModel ?? "" })
                break
              }
              case "permission.asked": {
                if (!sessionID) break
                ensureSession(sessionID)
                callHookSync("permission-blocked", { session_id: sessionID, model: currentModel ?? "" })
                break
              }
              case "permission.replied": {
                if (!sessionID) break
                callHookSync("active", { session_id: sessionID, model: currentModel ?? "" })
                break
              }
            }
          } catch {
            // a single malformed event must never break the stream
          }
        }
      } catch {
        // stream closed / errored — nothing safe left to do
      }
    })()

    // Tool execution is direct activity; the input carries sessionID at top level.
    void ctx.tool.hook("execute.before", (input: any) => {
      if (input?.sessionID) callHookSync("active", { session_id: input.sessionID, model: currentModel ?? "" })
    })
    void ctx.tool.hook("execute.after", (input: any) => {
      if (input?.sessionID) callHookSync("active", { session_id: input.sessionID, model: currentModel ?? "" })
    })

    return () => {}
  },
}
