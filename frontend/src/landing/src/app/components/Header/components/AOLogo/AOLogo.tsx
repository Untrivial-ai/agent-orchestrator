export function AOLogo() {
  return (
    <span
      aria-label="AO: Agent Orchestrator"
      className="inline-flex items-center gap-1 font-sans text-base font-medium leading-none tracking-[-0.5px] text-foreground"
    >
      {/* Center the mascot's body with the wordmark; the baton extends above it. */}
      <img
        src="/ao-logo.svg"
        alt=""
        width={20}
        height={20}
        aria-hidden="true"
        className="size-5 shrink-0 -translate-y-[3px]"
      />
      <span>AO: Agent Orchestrator</span>
    </span>
  );
}
