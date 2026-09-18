import { AGENT_HARNESSES, COMPANY } from "@ao/shared/constants";
import type { Metadata } from "next";
import Image from "next/image";
import { preload } from "react-dom";
import {
  formatStatPlus,
  getGitHubRepoStats,
  monthsSince,
} from "@/lib/github-stats";
import { DesignPartnerCta } from "./DesignPartnerCta";
import {
  RoadmapSlideshow,
  type RoadmapPhase,
} from "./RoadmapSlideshow";

const CONTACT_EMAIL = "prateek@untrivial.ai";
const CAL_URL = "https://cal.com/agentwrapper/ao-design-partner";
const MAILTO_HREF = `mailto:${CONTACT_EMAIL}?subject=${encodeURIComponent(
  "AO Design Partner Program",
)}&body=${encodeURIComponent(
  "Hi Prateek,\n\nWe're interested in the AO design partner program.\n\nCompany:\nEngineering team size:\nAgent harnesses we use today (Claude Code / Codex / Cursor / ...):\nWhat we want out of AO:\n",
)}`;
const HERO_IMAGE = "/optimized/design-partners/hero-car-engine-olive.webp";
const SHARED_WORKSPACE_IMAGE =
  "/optimized/design-partners/shared-workspace-olive.webp";

const ROADMAP_PHASE_IMAGES = {
  fleet: "/optimized/design-partners/roadmap-fleet-olive.webp",
  missionControl: "/optimized/design-partners/roadmap-mission-control-olive.webp",
  roi: "/optimized/design-partners/roadmap-roi-olive.webp",
  engineRoom: "/optimized/design-partners/roadmap-engine-room-olive.webp",
} as const;

/** Shown only if the GitHub API request fails. */
const FALLBACK_STATS = {
  stars: 8400,
  forks: 1200,
  months: 5,
} as const;

export const metadata: Metadata = {
  title: "Design Partner Program",
  description:
    "Mission control for your agent fleet: shared sessions, ROI observability, and an engine room your org fully owns. Your engineers get leverage; your leadership gets answers.",
  openGraph: {
    title: "AO Design Partner Program",
    description:
      "Mission control for your agent fleet: shared sessions, ROI observability, and an engine room your org fully owns.",
    url: `${COMPANY.MARKETING_URL}/design-partners`,
  },
  alternates: {
    canonical: `${COMPANY.MARKETING_URL}/design-partners`,
  },
};

const partnerGets = [
  {
    title: "White-glove onboarding",
    body: "Founder-led setup on your real repos, with the agents you already pay for.",
  },
  {
    title: "Weekly founder access",
    body: "A standing call and a private channel. Your bugs jump a queue that ships nightly.",
  },
  {
    title: "First access to every unlock",
    body: "Teams features land in your workspace before anyone else's.",
  },
  {
    title: "Locked-in pricing",
    body: "Six months of partner pricing at GA. Pilot fees credit toward year one.",
  },
];

const partnerAsks = [
  {
    title: "A paid pilot",
    body: "$500-2,000/month, sized to usage. Month-to-month. Churn is honest signal.",
  },
  {
    title: "A champion and a sponsor",
    body: "One engineer who runs AO weekly. One leader who wants the ROI answer.",
  },
  {
    title: "Real feedback",
    body: "A biweekly working session and honest numbers - including the bad news.",
  },
];

const phases: RoadmapPhase[] = [
  {
    num: "00",
    status: "available now",
    title: "A fleet on every desk",
    theme:
      "The single-player engine. Free, open source, already on your machine.",
    unlocks: [
      "23 harnesses behind one board - Claude Code, Codex, Cursor, and whatever comes next",
      "Every session in its own git worktree; branches never collide",
      "CI failures and review comments route back to the agent that owns the branch",
      "An orchestrator plans the work and spawns the workers",
    ],
    image: ROADMAP_PHASE_IMAGES.fleet,
    imageAlt:
      "One orchestration workstation coordinating five coding-agent modules across separate worktree lanes",
  },
  {
    num: "01",
    status: "building now - partners get it first",
    title: "Shared mission control",
    theme:
      "The fleet becomes a team. Execution stays local; coordination moves to one workspace.",
    unlocks: [
      "Team workspaces - the first cloud layer, opt-in by design",
      "Every session durably captured; hand a running fleet to a teammate",
      "One board for the team: running, merged, needs a human",
    ],
    image: ROADMAP_PHASE_IMAGES.missionControl,
    imageAlt:
      "Three local agent workstations connecting to a shared team mission-control board",
  },
  {
    num: "02",
    status: "partners shape the spec",
    title: "The ROI answer",
    theme:
      "What did the agents ship last week? What did it cost? Now you have the numbers.",
    unlocks: [
      "Token spend by project, agent, and team",
      "Outcomes, not vibes: agent PRs merged, cycle time, human-rescue rate",
      "Transcripts joined with GitHub, CI, reviews, and trackers - reviewable in one place",
    ],
    image: ROADMAP_PHASE_IMAGES.roi,
    imageAlt:
      "A measurement console joining agent cost, merged work, cycle time, and human handoffs",
  },
  {
    num: "03",
    status: "scoped with partners who need it",
    title: "The engine room",
    theme:
      "The whole engine inside your walls. Your sandboxes. Your policies. Your data.",
    unlocks: [
      "Self-hosted control plane in your VPC",
      "Security policies enforced on every agent; audit trails and SSO across the fleet",
      "The dataset stays yours: full transcripts joined with SCM, CI, and tracker history",
    ],
    image: ROADMAP_PHASE_IMAGES.engineRoom,
    imageAlt:
      "A self-hosted control plane, security gate, and archive contained inside a private perimeter",
  },
];

// The hero is excluded: its <Image priority> already preloads it, and as the LCP
// element it should stay the only image competing for early bandwidth. These are
// all below the fold, so they warm the cache at low priority instead.
const BELOW_FOLD_IMAGES = [
  SHARED_WORKSPACE_IMAGE,
  ...phases.map((phase) => phase.image),
];

function ArrowIcon({ className = "" }: { className?: string }) {
  return (
    <svg
      className={className}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.8"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
    >
      <path d="M5 12h14" />
      <path d="m13 6 6 6-6 6" />
    </svg>
  );
}

function SectionLabel({ children }: { children: React.ReactNode }) {
  return (
    <p className="text-sm font-medium tracking-[-0.5px] text-muted-foreground">
      {children}
    </p>
  );
}


export default async function DesignPartnersPage() {
  // Emitted before the stats await so the browser can fetch artwork in parallel.
  for (const image of BELOW_FOLD_IMAGES) {
    preload(image, { as: "image", fetchPriority: "low" });
  }

  const repo = await getGitHubRepoStats();
  const stars = repo?.stars ?? FALLBACK_STATS.stars;
  const forks = repo?.forks ?? FALLBACK_STATS.forks;
  const months = repo ? monthsSince(repo.createdAt) : FALLBACK_STATS.months;

  const stats = [
    {
      value: formatStatPlus(stars),
      label: `stars in ${months} month${months === 1 ? "" : "s"}`,
    },
    { value: formatStatPlus(forks), label: "forks" },
    { value: String(AGENT_HARNESSES), label: "agent harnesses" },
    { value: "Nightly", label: "desktop releases" },
  ];

  return (
    <main className="bg-background text-foreground">
      {/* Recording is bounded by this route: unmounting on navigation stops it. */}
      <section className="relative overflow-hidden px-4 pb-20 pt-24 sm:px-8 sm:pb-28 sm:pt-32 lg:px-[30px] lg:pt-36">
        <div className="relative mx-auto max-w-7xl">
          <div className="grid items-center gap-12 lg:grid-cols-[minmax(0,1fr)_minmax(0,0.82fr)] lg:gap-8">
            <div>
              <SectionLabel>Design partner program</SectionLabel>
              <h1 className="mt-5 max-w-4xl text-balance font-sans text-4xl font-normal leading-[0.98] tracking-[-0.5px] text-foreground sm:text-5xl md:text-6xl lg:text-[4.5rem]">
                Your company is the car. We build the engine.
              </h1>
              <p className="mt-6 max-w-3xl text-pretty text-base leading-8 text-muted-foreground sm:text-xl">
                Your engineers already run coding agents. Nobody runs the fleet.
                AO puts every agent, branch, and PR on one board, routes CI
                failures and review comments back to the agent that owns them,
                and tells you what the spend actually produced.
              </p>
              <div className="mt-8 flex flex-wrap gap-3">
                <DesignPartnerCta
                  href={CAL_URL}
                  external
                  className="inline-flex min-h-11 items-center gap-2 rounded-3xl bg-foreground px-6 py-3 text-base font-semibold tracking-[-0.5px] text-background transition-[transform,opacity] duration-150 hover:opacity-90 active:scale-[0.96] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-4 focus-visible:ring-offset-background motion-reduce:transition-none"
                >
                  Book a discovery call
                  <ArrowIcon className="h-4 w-4" />
                </DesignPartnerCta>
                <DesignPartnerCta
                  href={MAILTO_HREF}
                  className="inline-flex min-h-11 items-center gap-2 rounded-3xl border border-border bg-background px-6 py-3 text-base font-normal tracking-[-0.5px] text-foreground transition-[transform,background-color] duration-150 hover:bg-muted active:scale-[0.96] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-4 focus-visible:ring-offset-background motion-reduce:transition-none"
                >
                  or write to {CONTACT_EMAIL}
                </DesignPartnerCta>
              </div>
              <p className="mt-5 text-sm text-muted-foreground">
                paid pilots · month-to-month · cancel anytime
              </p>
            </div>

            <Image
              src={HERO_IMAGE}
              alt="An isometric car with its hood open and software engine highlighted"
              width={1536}
              height={1024}
              priority
              sizes="(max-width: 1024px) 100vw, 42vw"
              className="w-full"
            />
          </div>

          <div className="mt-16 grid overflow-hidden rounded-xl border border-border sm:grid-cols-2 lg:grid-cols-4">
            {stats.map((stat) => (
              <div
                key={stat.label}
                className="border-b border-border px-6 py-5 sm:border-r sm:[&:nth-child(2n)]:border-r-0 lg:border-b-0 lg:[&:nth-child(2n)]:border-r lg:last:border-r-0"
              >
                <p className="text-xl font-medium tracking-[-0.5px] text-foreground">
                  {stat.value}
                </p>
                <p className="mt-1 text-sm text-muted-foreground">{stat.label}</p>
              </div>
            ))}
          </div>
        </div>
      </section>

      <section className="border-t border-border px-4 py-16 sm:px-8 sm:py-20 lg:px-[30px]">
        <div className="mx-auto grid max-w-7xl gap-12 lg:grid-cols-[minmax(0,0.9fr)_minmax(0,1.1fr)]">
          <div>
            <SectionLabel>What your team gets</SectionLabel>
            <h2 className="mt-4 max-w-2xl text-3xl font-medium leading-tight tracking-[-0.5px] text-foreground sm:text-4xl lg:text-5xl">
              One workspace. Humans and agents, side by side.
            </h2>
            <p className="mt-6 max-w-xl text-base leading-8 text-muted-foreground">
              Every session shareable. Every outcome measurable. Every transcript
              captured - and owned by your org. Each layer lands in partner
              workspaces first.
            </p>
          </div>
          <Image
            src={SHARED_WORKSPACE_IMAGE}
            alt="Human workstations and coding-agent modules connected through one shared board and durable archive"
            width={1536}
            height={1024}
            sizes="(max-width: 1024px) 100vw, 704px"
            className="w-full"
          />
        </div>
      </section>

      <section className="border-t border-border px-4 py-16 sm:px-8 sm:py-20 lg:px-[30px]">
        <div className="mx-auto max-w-7xl">
          <div className="max-w-3xl">
            <SectionLabel>The exchange</SectionLabel>
            <h2 className="mt-4 text-3xl font-medium leading-tight tracking-[-0.5px] text-foreground sm:text-4xl lg:text-5xl">
              What you get. What we ask.
            </h2>
          </div>

          <div className="mt-12 grid overflow-hidden rounded-xl border border-border lg:grid-cols-2">
            <div className="lg:border-r lg:border-border">
              <p className="px-6 pb-2 pt-6 text-base font-medium tracking-[-0.5px] text-foreground sm:px-8 sm:pt-8">
                You get
              </p>
              <ul>
                {partnerGets.map((item) => (
                  <li
                    key={item.title}
                    className="px-6 py-3 text-sm leading-7 text-muted-foreground last:pb-6 sm:px-8 sm:last:pb-8"
                  >
                    <p className="font-medium text-foreground">{item.title}</p>
                    <p className="mt-1">{item.body}</p>
                  </li>
                ))}
              </ul>
            </div>
            <div className="border-t border-border lg:border-t-0">
              <p className="px-6 pb-2 pt-6 text-base font-medium tracking-[-0.5px] text-foreground sm:px-8 sm:pt-8">
                We ask
              </p>
              <ul>
                {partnerAsks.map((item) => (
                  <li
                    key={item.title}
                    className="px-6 py-3 text-sm leading-7 text-muted-foreground last:pb-6 sm:px-8 sm:last:pb-8"
                  >
                    <p className="font-medium text-foreground">{item.title}</p>
                    <p className="mt-1">{item.body}</p>
                  </li>
                ))}
              </ul>
            </div>
          </div>
          <p className="mt-8 max-w-4xl text-sm leading-7 text-muted-foreground">
            Best fit: 10-100 engineers. Multiple agent subscriptions in use.
            Leadership asking what all that agent spend actually produces. AO ships a new build every
            night - what we promise, you watch arrive.
          </p>
        </div>
      </section>

      <section className="border-t border-border px-4 py-16 sm:px-8 sm:py-20 lg:px-[30px]">
        <div className="mx-auto max-w-7xl">
          <SectionLabel>Roadmap</SectionLabel>
          <div className="mt-8">
            <RoadmapSlideshow phases={phases} />
          </div>
        </div>
      </section>

      <section className="border-t border-border px-4 py-16 sm:px-8 sm:py-20 lg:px-[30px]">
        <div className="mx-auto flex max-w-7xl flex-col items-start justify-between gap-8 py-8 lg:flex-row lg:items-center">
          <div>
            <SectionLabel>Design partner program</SectionLabel>
            <h2 className="mt-4 max-w-3xl text-3xl font-medium leading-tight tracking-[-0.5px] text-foreground sm:text-4xl">
              Your engineers get leverage. Your leadership gets answers.
            </h2>
          </div>
          <div className="flex shrink-0 flex-col gap-3 sm:flex-row">
            <DesignPartnerCta
              href={CAL_URL}
              external
              className="inline-flex min-h-11 items-center justify-center gap-2 rounded-3xl bg-foreground px-6 py-3 text-base font-semibold tracking-[-0.5px] text-background transition-[transform,opacity] duration-150 hover:opacity-90 active:scale-[0.96] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-4 focus-visible:ring-offset-background motion-reduce:transition-none"
            >
              Book a call
            </DesignPartnerCta>
            <DesignPartnerCta
              href={MAILTO_HREF}
              className="inline-flex min-h-11 items-center gap-2 text-foreground underline decoration-border underline-offset-4 transition-colors duration-150 hover:text-muted-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-4 focus-visible:ring-offset-background"
            >
              or write to {CONTACT_EMAIL}
            </DesignPartnerCta>
          </div>
        </div>
      </section>
    </main>
  );
}
