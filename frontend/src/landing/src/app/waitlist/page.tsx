import type { Metadata } from "next";
import Image from "next/image";
import { Cloud, GitBranch, Handshake } from "lucide-react";
import { CloudWaitlistForm } from "./CloudWaitlistForm";

export const metadata: Metadata = {
  title: "AO Cloud Waitlist",
  description:
    "Join the AO Cloud waitlist for hosted agent orchestration.",
};

const notes = [
  {
    icon: Cloud,
    label: "Hosted runs",
    text: "Run longer jobs on cloud capacity instead of one laptop.",
  },
  {
    icon: Handshake,
    label: "Shared workspaces",
    text: "Bring teammates into active sessions with clear ownership.",
  },
  {
    icon: GitBranch,
    label: "Same AO loop",
    text: "Keep branches, reviews, and CI connected to each run.",
  },
];

export default function WaitlistPage() {
  return (
    <main className="min-h-[100dvh] bg-background text-foreground">
      <section className="px-4 py-10 sm:px-8 sm:py-14 lg:px-[30px] lg:py-20">
        <div className="mx-auto grid max-w-7xl gap-6 lg:grid-cols-[minmax(0,1fr)_minmax(380px,460px)] lg:items-start xl:gap-8">
          <div className="relative order-2 overflow-hidden rounded-2xl border border-border bg-card lg:order-1 lg:h-[600px]">
            <Image
              src="/optimized/feature3.webp"
              alt="AO desktop workspace showing coordinated agent sessions"
              fill
              priority
              sizes="(max-width: 1023px) 100vw, 58vw"
              className="object-cover opacity-70"
            />
            <div className="absolute inset-0 bg-gradient-to-br from-background via-background/75 to-background/20" />
            <div className="relative flex min-h-[620px] flex-col justify-between p-5 sm:min-h-[560px] sm:p-8 lg:h-full lg:min-h-0 lg:p-10">
              <div className="max-w-3xl">
                <p className="inline-flex items-center gap-2 rounded-full border border-border bg-background/80 px-3 py-1 text-xs font-medium uppercase tracking-[0.18em] text-muted-foreground backdrop-blur">
                  <Cloud className="size-3.5" aria-hidden="true" />
                  AO Cloud
                </p>
                <h1 className="mt-5 max-w-4xl text-4xl font-semibold leading-[1.02] text-foreground sm:mt-6 sm:text-5xl lg:text-6xl">
                  Join the AO Cloud waitlist.
                </h1>
                <p className="mt-5 max-w-2xl text-base leading-7 text-muted-foreground sm:text-lg">
                  AO Cloud brings your agent work into a shared place your team
                  can follow, resume, and ship from.
                </p>
              </div>

              <div className="mt-8 grid gap-3 sm:mt-10 sm:grid-cols-3 lg:mt-12">
                {notes.map((note) => {
                  const Icon = note.icon;
                  return (
                    <div
                      key={note.label}
                      className="rounded-xl border border-border bg-background/80 p-3 backdrop-blur sm:p-4"
                    >
                      <Icon
                        className="size-4 text-muted-foreground"
                        aria-hidden="true"
                      />
                      <h2 className="mt-3 text-sm font-semibold text-foreground">
                        {note.label}
                      </h2>
                      <p className="mt-1 text-xs leading-5 text-muted-foreground">
                        {note.text}
                      </p>
                    </div>
                  );
                })}
              </div>
            </div>
          </div>

          <aside className="order-1 flex w-full flex-col rounded-2xl border border-border bg-card p-6 sm:p-8 lg:order-2 lg:h-[600px]">
            <div className="mb-7">
              <p className="text-xs font-medium uppercase tracking-[0.22em] text-muted-foreground">
                Early access
              </p>
              <h2 className="mt-3 text-2xl font-semibold text-foreground">
                Request your spot
              </h2>
              <p className="mt-3 text-sm leading-6 text-muted-foreground">
                We only need the basics right now: where to reach you and what
                role you hold at your company.
              </p>
            </div>
            <CloudWaitlistForm />
          </aside>
        </div>
      </section>
    </main>
  );
}
