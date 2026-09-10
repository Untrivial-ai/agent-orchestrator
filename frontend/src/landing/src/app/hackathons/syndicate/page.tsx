import { COMPANY } from "@ao/shared/constants";
import { ArrowUpRight, GitBranch, Sparkles, Users } from "lucide-react";
import type { Metadata } from "next";

const eventUrl = "https://luma.com/embed/event/evt-gkxbXap1DCJThsE/simple";
const pageUrl = `${COMPANY.MARKETING_URL}/hackathons/syndicate/`;
const lumaUrl = "https://luma.com/event/evt-gkxbXap1DCJThsE";
const participantPassUrl = `${COMPANY.MARKETING_URL}/hackathons/syndicate/pass/`;

const highlights = [
  {
    icon: GitBranch,
    label: "Build in branches",
    text: "Bring an idea, split the work, and let agent sessions move in parallel.",
  },
  {
    icon: Users,
    label: "Ship with a crew",
    text: "A focused community sprint for builders exploring agent orchestration.",
  },
  {
    icon: Sparkles,
    label: "Demo real output",
    text: "Make something concrete enough to show, review, and keep iterating on.",
  },
] as const;

export const metadata: Metadata = {
  title: "Syndicate Hackathon",
  description: "Register for the AO Syndicate hackathon.",
  openGraph: {
    type: "website",
    url: pageUrl,
    siteName: COMPANY.NAME,
    title: `Syndicate Hackathon | ${COMPANY.NAME}`,
    description: "Register for the AO Syndicate hackathon.",
    images: [
      {
        url: `${COMPANY.MARKETING_URL}/og-image.png`,
        width: 1200,
        height: 630,
        alt: `${COMPANY.NAME} Syndicate hackathon`,
      },
    ],
  },
  twitter: {
    card: "summary_large_image",
    site: "@aoagents",
    title: `Syndicate Hackathon | ${COMPANY.NAME}`,
    description: "Register for the AO Syndicate hackathon.",
    images: [`${COMPANY.MARKETING_URL}/og-image.png`],
  },
  alternates: {
    canonical: pageUrl,
  },
};

export default function SyndicateHackathonPage() {
  return (
    <main className="min-h-[100dvh] overflow-hidden bg-background font-sans text-foreground">
      <section className="relative px-4 pb-16 pt-16 sm:px-8 sm:pb-20 sm:pt-20 lg:px-[30px] lg:pb-24 lg:pt-24">
        <div className="mx-auto max-w-7xl">
          <div className="grid items-center gap-10 lg:grid-cols-[minmax(0,0.92fr)_minmax(460px,600px)] lg:gap-14">
            <div className="max-w-3xl">
              <h1 className="max-w-4xl text-balance text-[clamp(42px,8vw,92px)] font-normal leading-[0.96] tracking-normal text-foreground">
                Syndicate Hackathon
              </h1>
              <p className="mt-6 max-w-2xl text-pretty text-lg leading-8 text-muted-foreground sm:text-xl">
                A build sprint for people using coding agents as a team sport.
                Register on Luma and bring an idea worth handing to a fleet.
              </p>

              <div className="mt-8 flex flex-wrap gap-3">
                <a
                  href={participantPassUrl}
                  target="_blank"
                  rel="noreferrer"
                  className="inline-flex items-center gap-2 rounded-3xl bg-primary px-5 py-3 text-sm font-medium text-primary-foreground transition-colors hover:bg-primary/90"
                >
                  Get your participant pass
                  <ArrowUpRight className="size-4" aria-hidden="true" />
                </a>
                <a
                  href={lumaUrl}
                  target="_blank"
                  rel="noreferrer"
                  className="inline-flex items-center gap-2 rounded-3xl border border-border px-5 py-3 text-sm font-medium text-foreground transition-colors hover:bg-muted"
                >
                  Open event on Luma
                  <ArrowUpRight className="size-4" aria-hidden="true" />
                </a>
              </div>
            </div>

            <div className="relative">
              <div className="relative overflow-hidden rounded-[8px] border border-border bg-card/90 shadow-2xl shadow-black/35 backdrop-blur">
                <div className="flex items-center justify-between border-b border-border px-4 py-3 sm:px-5">
                  <div>
                    <h2 className="text-base font-medium tracking-normal text-foreground">
                      Reserve your spot
                    </h2>
                  </div>
                  <a
                    href={lumaUrl}
                    target="_blank"
                    rel="noreferrer"
                    aria-label="Open Syndicate Hackathon on Luma"
                    className="inline-flex size-9 items-center justify-center rounded-full border border-border text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
                  >
                    <ArrowUpRight className="size-4" aria-hidden="true" />
                  </a>
                </div>
                <iframe
                  src={eventUrl}
                  title="AO Syndicate hackathon registration"
                  width="600"
                  height="450"
                  className="block h-[520px] w-full bg-background sm:h-[560px] lg:h-[600px]"
                  allow="fullscreen; payment"
                  sandbox="allow-forms allow-popups allow-same-origin allow-scripts allow-top-navigation-by-user-activation"
                  aria-hidden="false"
                />
              </div>
            </div>
          </div>

          <div className="mt-14 grid gap-3 md:grid-cols-3">
            {highlights.map((item) => {
              const Icon = item.icon;
              return (
                <section
                  key={item.label}
                  className="rounded-[8px] border border-border bg-card/70 p-5 backdrop-blur"
                >
                  <Icon className="size-5 text-brand-light" aria-hidden="true" />
                  <h2 className="mt-5 text-lg font-medium tracking-normal text-foreground">
                    {item.label}
                  </h2>
                  <p className="mt-2 text-sm leading-6 text-muted-foreground">
                    {item.text}
                  </p>
                </section>
              );
            })}
          </div>
        </div>
      </section>
    </main>
  );
}
