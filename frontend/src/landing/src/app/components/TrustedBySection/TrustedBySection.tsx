type Agent = { name: string; src: string };

// All 25 supported agents, each with its brand logo. Most come from the app's
// agent assets; goose uses a whitened mark and agy (Antigravity) / auggie
// (Augment) / autohand use their own brand favicons so every mark reads on
// the dark background. Copilot and Codex marks come from the LobeHub icon
// set; Kilo Code uses the Kilo-Org brand tile.
const AGENTS: Agent[] = [
  { name: "Claude Code", src: "/app-icons/agents/claude-code.svg" },
  { name: "Codex", src: "/app-icons/agents/codex.svg" },
  { name: "Cursor", src: "/app-icons/agents/cursor.svg" },
  { name: "OpenCode", src: "/app-icons/agents/opencode.svg" },
  { name: "Copilot", src: "/app-icons/agents/copilot-color.svg" },
  { name: "Aider", src: "/app-icons/agents/aider.png" },
  { name: "Grok", src: "/app-icons/agents/grok.png" },
  { name: "Droid", src: "/app-icons/agents/droid.png" },
  { name: "Crush", src: "/app-icons/agents/crush.png" },
  { name: "Qwen", src: "/app-icons/agents/qwen.png" },
  { name: "Goose", src: "/app-icons/agents/goose.svg" },
  { name: "Continue", src: "/app-icons/agents/continue.png" },
  { name: "Devin", src: "/app-icons/agents/devin.png" },
  { name: "Kimi", src: "/app-icons/agents/kimi.png" },
  { name: "Muse Code", src: "/app-icons/agents/muse.png" },
  { name: "Kiro", src: "/app-icons/agents/kiro.png" },
  { name: "Kilo Code", src: "/app-icons/agents/kilocode.png" },
  { name: "Mistral Vibe", src: "/app-icons/agents/vibe.png" },
  { name: "Pi", src: "/app-icons/agents/pi.png" },
  { name: "Kimchi", src: "/docs/logos/kimchi.svg" },
  { name: "Amp", src: "/app-icons/agents/amp.svg" },
  { name: "Cline", src: "/app-icons/agents/cline.svg" },
  { name: "Antigravity", src: "/app-icons/agents/agy.png" },
  { name: "Auggie", src: "/app-icons/agents/auggie.svg" },
  { name: "Autohand", src: "/app-icons/agents/autohand.svg" },
];

function AgentMark({ agent }: { agent: Agent }) {
  return (
    <img
      src={agent.src}
      alt={agent.name}
      title={agent.name}
      className="h-8 w-8 shrink-0 object-contain"
      draggable="false"
    />
  );
}

// One full pass of the logos as an equal-width group with a trailing gap equal
// to the inner gap, so two groups tile seamlessly and translateX(-50%) lands
// exactly on the second group (no reset jump). The duplicate group is
// aria-hidden so screen readers meet each agent once.
function AgentGroup({ duplicate = false }: { duplicate?: boolean }) {
  return (
    <ul
      aria-hidden={duplicate || undefined}
      className="agent-marquee__group flex shrink-0 items-center gap-8 pr-8 sm:gap-10 sm:pr-10"
    >
      {AGENTS.map((agent) => (
        <li key={agent.name}>
          <AgentMark agent={agent} />
        </li>
      ))}
    </ul>
  );
}

export function TrustedBySection() {
  return (
    <section
      id="agents"
      className="py-16 sm:py-24 bg-background overflow-hidden"
    >
      <div className="max-w-7xl mx-auto text-center">
        <h2 className="mx-auto mb-12 max-w-3xl select-none px-4 text-3xl font-semibold text-foreground sm:px-8 sm:text-4xl lg:max-w-none lg:px-[30px] lg:text-5xl">
          Use the agents you already trust.
        </h2>

        {/* Animated marquee: ~10 logos visible as they flow. Pauses on hover;
            reduced-motion users get the full static list below. */}
        <div className="agent-marquee group relative mx-auto w-full max-w-2xl overflow-hidden [mask-image:linear-gradient(to_right,transparent,black_8%,black_92%,transparent)] lg:max-w-5xl">
          <div className="agent-marquee__track flex w-max">
            <AgentGroup />
            <AgentGroup duplicate />
          </div>
        </div>

        {/* Reduced-motion fallback: every agent, wrapping, fully visible. */}
        <ul className="agent-static mx-auto hidden w-full max-w-4xl flex-wrap items-center justify-center gap-x-6 gap-y-5 px-4 sm:gap-8">
          {AGENTS.map((agent) => (
            <li key={agent.name}>
              <AgentMark agent={agent} />
            </li>
          ))}
        </ul>
      </div>

      <style>{`
        @keyframes agent-marquee-scroll {
          from { transform: translateX(0); }
          to { transform: translateX(-50%); }
        }
        .agent-marquee__track {
          animation: agent-marquee-scroll 45s linear infinite;
          will-change: transform;
        }
        .agent-marquee:hover .agent-marquee__track {
          animation-play-state: paused;
        }
        @media (prefers-reduced-motion: reduce) {
          .agent-marquee { display: none; }
          .agent-static { display: flex; }
        }
      `}</style>
    </section>
  );
}
