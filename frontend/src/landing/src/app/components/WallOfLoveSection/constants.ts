import { COMPANY } from "@ao/shared/constants";

export interface Testimonial {
  id: string;
  author: string;
  handle?: string;
  avatar?: string;
  role?: string;
  content: string;
  originalContent?: string;
  url: string;
}

export const TESTIMONIALS: Testimonial[] = [
  {
    id: "akash-parashar",
    author: "Akash Parashar",
    handle: "@itsakaashhh",
    content:
      "I came across AO while using it during a hackathon. It was simple enough for me to get used to it within a couple hours. And I am glad I did. I had never handled more than one agent before. It handled context across workers it spawned and it could even communicate with other orchestrators in the workspace.\n\nThinking of the agents' work as separate git worktrees also kept my mind at peace, because at the end, the decision is with me, whether I want to merge the branch or not.",
    avatar: "/testimonials/akash-parashar.webp",
    url: "https://x.com/itsakaashhh/status/2087531052797657135?s=20",
  },
  {
    id: "bhavit-sharma",
    author: "Bhavit Sharma",
    handle: "LinkedIn",
    content:
      "AO helped me in removing the friction of using multiple harnesses at the same time. I can just work in peace without ever worrying about running grok, omp, or opencode CLI separately. I get the best of all worlds!",
    avatar: "https://unavatar.io/linkedin/bhavit-sharma",
    role: "xAI",
    url: "https://www.linkedin.com/in/bhavit-sharma",
  },
  {
    id: "harshit-singh-bhandari",
    author: "Harshit Singh Bhandari",
    content:
      "Before AO, I would ship at most 2–3 PRs a day. Now I consistently ship 5+ PRs every day at work.",
    avatar: "/testimonials/harshit-singh-bhandari.webp",
    role: "IEOR @ IIT Bombay",
    url: "/testimonials/",
  },
  {
    id: "aditya-purohit",
    author: "Aditya Purohit",
    content:
      "AO automatically gets the right agent to address CI failures and review comments. My agents are much more autonomous now, and with the orchestrator + kanban, I’m able to manage more and more of them.",
    avatar: "/testimonials/aditya-purohit.webp",
    role: "CTO @ Osvi.ai",
    url: "/testimonials/",
  },
  {
    id: "aditi-chauhan",
    author: "Aditi Chauhan",
    content:
      "AO really changes the way you develop. The orchestrator and kanban have been a game changer. I’m no longer confused about what agent is doing what; scoping tasks and spawning them off has been a breeze.",
    role: "Software Engineer, Docusign",
    url: "/testimonials/",
  },
  {
    id: "buchireddy",
    author: "Buchi Reddy B",
    handle: "@buchireddy",
    content:
      "I really loved the building blocks present in @aoagents, hence we went all-in on that pretty early. Happy to share more details if it helps others.",
    avatar: "https://unavatar.io/x/buchireddy",
    url: "https://x.com/buchireddy/status/2064108144607760628",
  },
];
