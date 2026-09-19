export interface Feature {
  tag: string;
  title: string;
  description: string;
  /** Keyword-rich internal link for SEO and on-page discovery. */
  learnMore?: { href: string; label: string };
  colors: readonly [string, string, string, string];
}

export const FEATURES: Feature[] = [
  {
    tag: "Delegation",
    title: "Tell the orchestrator what you need",
    description:
      "Every project gets a main agent that runs the fleet for you. Describe the outcome, it plans the work, spawns workers into their own worktrees, keeps them moving, and escalates only what needs a human.",
    learnMore: {
      href: "/docs/tutorials/what-is-an-agent-orchestrator/",
      label: "What is an agent orchestrator",
    },
    colors: ["#7f1d1d", "#991b1b", "#450a0a", "#1a1a2e"],
  },
  {
    tag: "Visibility",
    title: "The whole fleet, at a glance",
    description:
      "Every session flows across one board, working, needs you, in review, ready to merge, with its agent, branch, and PR state on the card. You see what needs attention and ignore the rest.",
    learnMore: {
      href: "/docs/tutorials/run-multiple-claude-code-agents-in-parallel/",
      label: "Run multiple Claude Code agents in parallel",
    },
    colors: ["#047857", "#065f46", "#064e3b", "#1a1a2e"],
  },
  {
    tag: "Feedback Loop",
    title: "CI fails. The right agent fixes it.",
    description:
      "AO watches every PR it opens. Failed checks and review comments route back to the session that owns the branch, with the context to fix them, until the PR is approved.",
    learnMore: {
      href: "/docs/tutorials/git-worktrees-for-coding-agents/",
      label: "Git worktrees for coding agents",
    },
    colors: ["#1e40af", "#1e3a8a", "#172554", "#1a1a2e"],
  },
  {
    tag: "Coverage",
    title: "Use the agent you already trust",
    description:
      "27 harnesses supported, with per-project agent choice. Claude Code, Codex, Cursor, OpenCode, Aider, Goose, and more, AO keeps it running the way you already run it while the tools underneath evolve.",
    learnMore: {
      href: "/docs/tutorials/how-ao-handles-provider-policy-changes/",
      label: "How AO handles provider policy changes",
    },
    colors: ["#7c3aed", "#6d28d9", "#4c1d95", "#1a1a2e"],
  },
  {
    tag: "Mobile companion",
    title: "Your fleet goes where you go",
    description:
      "Pair the AO mobile app with your desktop over LAN or Tailscale. Watch every session, open a terminal, and get notified when an agent needs you - while execution and code stay on your machine.",
    learnMore: {
      href: "/docs/tutorials/run-coding-agents-from-your-phone/",
      label: "Run coding agents from your phone",
    },
    colors: ["#2563eb", "#1d4ed8", "#1e3a8a", "#1a1a2e"],
  },
];
