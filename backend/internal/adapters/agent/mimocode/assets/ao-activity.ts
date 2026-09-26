// agent-orchestrator: managed mimo-code activity hook (do not edit)

const HOOK_TIMEOUT_MS = 30_000
const promptReports = new Map<string, boolean>()
const messageStore = new Map<string, any>()
let currentSessionID: string | null = null
let currentModel: string | null = null
const launchID = (process.env.AO_RUNTIME_LAUNCH_ID ?? "").trim()

function hookCommand(name: string): string[] | null {
  const ao = Bun.which("ao")
  return ao ? [ao, "hooks", "mimo-code", name] : null
}

function callHook(name: string, payload: Record<string, unknown>) {
  try {
    const command = hookCommand(name)
    if (!command) return
    Bun.spawnSync(command, {
      cwd: process.cwd(),
      env: { ...process.env, AO_RUNTIME_LAUNCH_ID: launchID },
      stdin: new TextEncoder().encode(JSON.stringify({ ...payload, launch_id: launchID }) + "\n"),
      stdout: "ignore",
      stderr: "ignore",
      timeout: HOOK_TIMEOUT_MS,
    })
  } catch {
    // Activity reporting must never break the user's MiMo Code session.
  }
}

function sessionID(value: any): string | null {
  const id = value?.sessionID ?? value?.sessionId ?? value?.session_id
  return typeof id === "string" && id.trim() ? id.trim() : null
}

function createdSessionID(value: any): string | null {
  const id = sessionID(value) ?? value?.id
  return typeof id === "string" && id.trim() ? id.trim() : null
}

function switchSession(id: string) {
  if (currentSessionID === id) return false
  currentSessionID = id
  currentModel = null
  promptReports.clear()
  messageStore.clear()
  return true
}

function reportPrompt(id: string, messageID: string, prompt: string) {
  const hasText = prompt.length > 0
  const previous = promptReports.get(messageID)
  if (previous || (previous === false && !hasText)) return
  promptReports.set(messageID, hasText)
  callHook("user-prompt-submit", { session_id: id, prompt, model: currentModel ?? "" })
}

export default {
  "permission.ask": async (input: any) => {
    const id = sessionID(input) ?? currentSessionID
    if (id) callHook("permission-blocked", { session_id: id, model: currentModel ?? "" })
  },
  event: async ({ event }: any) => {
    try {
      switch (event.type) {
        case "session.created": {
          const id = createdSessionID(event.properties?.info)
          if (id && switchSession(id)) callHook("session-start", { session_id: id })
          break
        }
        case "message.updated": {
          const message = event.properties?.info
          if (!message) break
          const id = sessionID(message)
          if (id && switchSession(id)) callHook("session-start", { session_id: id })
          if (message.role === "assistant" && message.modelID) currentModel = message.modelID
          if (message.role === "user") {
            messageStore.set(message.id, message)
            const activeID = id ?? currentSessionID
            if (activeID) reportPrompt(activeID, message.id, "")
          }
          break
        }
        case "message.part.updated": {
          const part = event.properties?.part
          const message = part?.messageID ? messageStore.get(part.messageID) : undefined
          if (message?.role === "user" && part.type === "text") {
            const id = sessionID(message) ?? currentSessionID
            if (id) reportPrompt(id, message.id, part.text ?? "")
            if (part.text) messageStore.delete(part.messageID)
          }
          break
        }
        case "session.status": {
          if (event.properties?.status?.type !== "idle") break
          const id = event.properties?.sessionID ?? currentSessionID
          if (id) callHook("stop", { session_id: id, model: currentModel ?? "" })
          break
        }
        case "permission.asked":
        case "question.asked": {
          const id = event.properties?.sessionID ?? currentSessionID
          if (id) callHook("permission-blocked", { session_id: id, model: currentModel ?? "" })
          break
        }
        case "permission.replied":
        case "question.replied":
        case "question.rejected": {
          const id = event.properties?.sessionID ?? currentSessionID
          if (id) callHook("active", { session_id: id, model: currentModel ?? "" })
          break
        }
      }
    } catch {
      // Malformed provider events are observation failures, not session failures.
    }
  },
  "tool.execute.before": async (input: any) => {
    callHook("active", { session_id: input.sessionID, model: currentModel ?? "" })
  },
  "tool.execute.after": async (input: any) => {
    callHook("active", { session_id: input.sessionID, model: currentModel ?? "" })
  },
}
