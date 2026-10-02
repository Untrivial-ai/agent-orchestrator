// agent-orchestrator: managed opencode-v2 activity plugin (do not edit)
import { spawn } from "node:child_process"

const HOOK_TIMEOUT_MS = 1_250

export default {
  id: "agent-orchestrator.activity.v2",
  async setup(context) {
    const launchID = (process.env.AO_RUNTIME_LAUNCH_ID ?? "").trim()
    const seenSessions = new Set()
    const controller = new AbortController()

    let queue = Promise.resolve()

    function send(event, sessionID, payload) {
      return new Promise((resolve) => {
        try {
          const child = spawn("ao", ["hooks", "opencode-v2", event], {
            cwd: context.location.directory,
            env: { ...process.env, AO_RUNTIME_LAUNCH_ID: launchID },
            stdio: ["pipe", "ignore", "ignore"],
          })
          const timer = setTimeout(() => child.kill(), HOOK_TIMEOUT_MS)
          const done = () => {
            clearTimeout(timer)
            resolve()
          }
          child.on("error", done)
          child.on("close", done)
          child.stdin.on("error", () => {})
          child.stdin.end(JSON.stringify({ ...payload, session_id: sessionID, launch_id: launchID }) + "\n")
        } catch {
          resolve()
        }
      })
    }

    // Activity is observational: report in order but asynchronously so it never blocks OpenCode.
    function report(event, sessionID, payload = {}) {
      if (!sessionID) return
      queue = queue.then(() => send(event, sessionID, payload))
    }

    function ensureSession(sessionID) {
      if (!sessionID || seenSessions.has(sessionID)) return
      seenSessions.add(sessionID)
      report("session-start", sessionID)
    }

    function handleEvent(event) {
      const data = event?.data
      const sessionID = data?.sessionID
      switch (event?.type) {
        case "session.created":
          ensureSession(sessionID)
          break
        case "session.execution.started":
          ensureSession(sessionID)
          report("active", sessionID)
          break
        case "session.execution.succeeded":
        case "session.execution.failed":
        case "session.execution.interrupted":
          ensureSession(sessionID)
          report("stop", sessionID)
          break
        case "session.status":
          ensureSession(sessionID)
          if (data?.status?.type === "idle") report("stop", sessionID)
          else if (data?.status?.type === "busy") report("active", sessionID)
          break
        case "permission.asked":
          ensureSession(sessionID)
          report("permission-blocked", sessionID, { permission_id: data?.id ?? "" })
          break
        case "permission.replied":
          ensureSession(sessionID)
          report("permission-resolved", sessionID, { permission_id: data?.requestID ?? "", reply: data?.reply ?? "" })
          break
      }
    }

    const eventLoop = (async () => {
      try {
        for await (const event of context.event.subscribe({ signal: controller.signal })) handleEvent(event)
      } catch {
        // Event delivery and AO reporting are both best effort.
      }
    })()

    const registrations = await Promise.all([
      context.tool.hook("execute.before", (input) => {
        ensureSession(input.sessionID)
        report("active", input.sessionID, { tool_name: input.tool ?? "", tool_use_id: input.id ?? "" })
      }),
      context.tool.hook("execute.after", (input) => {
        ensureSession(input.sessionID)
        report("active", input.sessionID, { tool_name: input.tool ?? "", tool_use_id: input.id ?? "" })
      }),
    ])

    return async () => {
      controller.abort()
      await queue
      await Promise.allSettled(registrations.map((registration) => registration.dispose()))
      await eventLoop
    }
  },
}
