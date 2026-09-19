export interface FAQItem {
  question: string;
  answer: string;
  related?: { href: string; label: string };
}

export const FAQ_ITEMS: FAQItem[] = [
  {
    question: "I already use an IDE like Cursor, is this for me?",
    answer:
      "AO is designed to work with your existing tool. We natively support deep-linking to IDEs like Cursor so you can open your workspaces and files in your IDE. AO sits above individual tools, use whatever agent you like, AO keeps the workflow the same.",
    related: {
      href: "/docs/tutorials/what-is-an-agent-orchestrator/",
      label: "What is an agent orchestrator",
    },
  },
  {
    question: "Which AI coding agents are supported?",
    answer:
      "AO works with any CLI-based coding agent including Claude Code, OpenCode, OpenAI Codex, Cursor, Aider, Goose, and many more. If it runs in a terminal, it runs in AO. 27 harnesses total, with per-project agent choice.",
    related: {
      href: "/docs/tutorials/how-ao-handles-provider-policy-changes/",
      label: "How AO handles provider policy changes",
    },
  },
  {
    question: "How does the parallel agent system work?",
    answer:
      "Each agent runs in its own isolated Git worktree, which means they can work on different branches or features simultaneously without conflicts. AO's orchestrator spawns workers, routes CI failures and review feedback to the right session, and lets you monitor the entire fleet from one board.",
    related: {
      href: "/docs/tutorials/run-multiple-claude-code-agents-in-parallel/",
      label: "Run multiple Claude Code agents in parallel",
    },
  },
  {
    question: "Is Agent Orchestrator free to use?",
    answer:
      "Yes. AO is free and open source under Apache 2.0. It runs as a local daemon on your machine, your code never leaves localhost. No account, no cloud, no credit card required.",
  },
  {
    question: "Can I use my own API keys?",
    answer:
      "Absolutely. AO doesn't proxy any API calls. You use your own API keys directly with whatever AI providers you choose. This means you have full control over costs and usage.",
    related: {
      href: "/docs/tutorials/how-ao-handles-provider-policy-changes/",
      label: "How AO handles provider policy changes",
    },
  },
];
