export function visibleProductName(text: string): string {
  return text.replaceAll("Agent Orchestrator", "Orchestrator").replace(/\bAO\b/g, "Orchestrator");
}
