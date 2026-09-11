import { COMPANY } from "@ao/shared/constants";
import { ArrowRight, ArrowUpRight } from "lucide-react";
import type { Metadata } from "next";
import Link from "next/link";
import { TextScramble } from "./TextScramble";

const pageUrl = `${COMPANY.MARKETING_URL}/hackathons/`;

export const metadata: Metadata = {
  title: "AO Hackathons",
  description:
    "Join upcoming AO hackathons and explore past community build sprints.",
  openGraph: {
    type: "website",
    url: pageUrl,
    siteName: COMPANY.NAME,
    title: `AO Hackathons | ${COMPANY.NAME}`,
    description:
      "Join upcoming AO hackathons and explore past community build sprints.",
    images: [
      {
        url: `${COMPANY.MARKETING_URL}/og-image.png`,
        width: 1200,
        height: 630,
        alt: `${COMPANY.NAME} hackathons`,
      },
    ],
  },
  twitter: {
    card: "summary_large_image",
    site: "@ao_build",
    title: `AO Hackathons | ${COMPANY.NAME}`,
    description:
      "Join upcoming AO hackathons and explore past community build sprints.",
    images: [`${COMPANY.MARKETING_URL}/og-image.png`],
  },
  alternates: {
    canonical: pageUrl,
  },
};

export default function HackathonsPage() {
  return (
    <main className="min-h-[100dvh] overflow-hidden bg-background font-sans text-foreground">
      <section className="relative px-4 pb-16 pt-16 sm:px-8 sm:pb-20 sm:pt-20 lg:px-[30px] lg:pb-24 lg:pt-24">
        <div className="mx-auto max-w-7xl">
          <div className="max-w-4xl">
            <h1 className="relative text-balance text-[clamp(44px,8vw,96px)] font-normal leading-[1.02] tracking-normal text-foreground">
              <TextScramble text="Build with agents, then show the work." />
            </h1>
            <p className="mt-6 max-w-2xl text-pretty text-lg leading-8 text-muted-foreground sm:text-xl">
              Community build sprints for people turning AO into their coding
              workspace. Join the next run or look back at what builders already
              shipped.
            </p>
          </div>

          <div className="mt-12 grid gap-5 lg:grid-cols-[minmax(0,1.05fr)_minmax(360px,0.95fr)] lg:items-stretch">
            <article className="group relative overflow-hidden rounded-[8px] border border-border bg-card/85 backdrop-blur">
              <div className="flex min-h-[520px] flex-col justify-between p-6 sm:p-8 lg:p-10">
                <div>
                  <h2 className="max-w-2xl text-balance text-[clamp(34px,5vw,64px)] font-normal leading-[0.98] tracking-normal text-foreground">
                    Syndicate Hackathon
                  </h2>
                  <p className="mt-5 max-w-xl text-base leading-7 text-muted-foreground sm:text-lg">
                    A build sprint for people using coding agents as a team
                    sport. Bring an idea worth handing to a fleet.
                  </p>
                </div>

                <Link
                  href="/hackathons/syndicate"
                  className="mt-10 inline-flex w-fit items-center gap-2 rounded-3xl bg-primary px-5 py-3 text-sm font-medium text-primary-foreground transition-colors hover:bg-primary/90"
                >
                  View Syndicate
                  <ArrowRight className="size-4" aria-hidden="true" />
                </Link>
              </div>
            </article>

            <article className="rounded-[8px] border border-border bg-card/75 p-6 backdrop-blur sm:p-8 lg:p-10">
              <div className="flex items-start justify-between gap-4">
                <div>
                  <h2 className="text-3xl font-normal leading-tight tracking-normal text-foreground sm:text-4xl">
                    The Orchestra
                  </h2>
                </div>
                <a
                  href="https://luma.com/iw1v5erp"
                  target="_blank"
                  rel="noreferrer"
                  aria-label="Open The Orchestra on Luma"
                  className="inline-flex size-10 shrink-0 items-center justify-center rounded-full border border-border text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
                >
                  <ArrowUpRight className="size-4" aria-hidden="true" />
                </a>
              </div>

              <p className="mt-5 text-base leading-7 text-muted-foreground">
                AO's first hackathon was a fully online sprint with no fixed
                theme. Builders used AO to plan, delegate, code, review, test,
                and ship with agents running on their own machines.
              </p>

            </article>
          </div>
        </div>
      </section>
    </main>
  );
}
