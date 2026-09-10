// agent-orchestrator: managed Prime Agent activity extension
import { spawnSync } from "node:child_process";

const hookTimeoutMs = 10_000;

function report(event, payload, cwd) {
  try {
    spawnSync("ao", ["hooks", "prime-agent", event], {
      cwd,
      encoding: "utf8",
      input: `${JSON.stringify(payload)}\n`,
      stdio: ["pipe", "ignore", "ignore"],
      timeout: hookTimeoutMs,
      windowsHide: true,
    });
  } catch {
    // Activity reporting is best effort and must never break Prime Agent.
  }
}

function bestEffort(handler) {
  return (...args) => {
    try {
      handler(...args);
    } catch {
      // Malformed lifecycle data must not escape into Prime Agent.
    }
  };
}

function nativeSessionId(context) {
  const getSessionId = context.sessionManager?.getSessionId;
  if (typeof getSessionId !== "function") {
    return "";
  }
  const sessionId = getSessionId.call(context.sessionManager);
  return typeof sessionId === "string" ? sessionId : "";
}

export default function aoActivityExtension(prime) {
  let pendingPrompt = "";

  prime.on("session_start", bestEffort((event, context) => {
    const payload = { reason: event.reason ?? "" };
    const sessionId = nativeSessionId(context);
    if (sessionId !== "") {
      payload.session_id = sessionId;
    }
    report("session-start", payload, context.cwd);
  }));

  prime.on("before_agent_start", bestEffort((event) => {
    pendingPrompt = typeof event.prompt === "string" ? event.prompt : "";
  }));

  prime.on("agent_start", bestEffort((_event, context) => {
    const prompt = pendingPrompt;
    pendingPrompt = "";
    report("user-prompt-submit", { prompt }, context.cwd);
  }));

  prime.on("agent_end", bestEffort((_event, context) => {
    report("stop", {}, context.cwd);
  }));

  prime.on("session_shutdown", bestEffort((event, context) => {
    pendingPrompt = "";
    if (event.reason === "quit") {
      report("session-end", { reason: event.reason }, context.cwd);
    }
  }));
}
