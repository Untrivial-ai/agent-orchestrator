import type { Metadata } from "next";
import Image from "next/image";
import { Bricolage_Grotesque } from "next/font/google";
import { Quote } from "lucide-react";
import { TestimonialForm } from "./TestimonialForm";

const testimonialDisplay = Bricolage_Grotesque({
  weight: "600",
  subsets: ["latin"],
  display: "swap",
});

export const metadata: Metadata = {
  title: "Share Your AO Story",
  description:
    "Submit your Agent Orchestrator testimonial for the AO website.",
};

const testimonials = [
  {
    quote:
      "AO really changes the way you develop. The orchestrator and kanban have been a game changer. I’m no longer confused about what agent is doing what; scoping tasks and spawning them off has been a breeze.",
    author: "Aditi Chauhan, Software Engineer, Docusign",
  },
  {
    quote:
      "With AO Mobile, I’m able to ship things on the fly, and my agents are never blocked on my input anymore.",
    author: "Dhruv Sharma, Engineering Lead, The Hashgraph group",
  },
  {
    quote:
      "Before AO, I would ship at most 2–3 PRs a day. Now I consistently ship 5+ PRs every day at work.",
    author: "Harshit Singh Bhandari, IEOR @ IIT Bombay",
  },
  {
    quote:
      "There hasn’t been a day in the last two months when I opened another IDE or ran a coding agent in a terminal app. AO really changes how you think about work. It’s a mindset shift you can’t go back from.",
    author: "Pritom Mazumdar, Microsoft",
  },
  {
    quote:
      "AO automatically gets the right agent to address CI failures and review comments. My agents are much more autonomous now, and with the orchestrator + kanban, I’m able to manage more and more of them.",
    author: "Aditya Purohit, CTO @ Osvi.ai",
  },
];

export default function TestimonialsPage() {
  return (
    <main className="min-h-[100dvh] bg-background text-foreground">
      <section className="px-4 py-10 sm:px-8 sm:py-14 lg:px-[30px] lg:py-20">
        <div className="mx-auto grid max-w-7xl gap-6 lg:grid-cols-2 lg:items-start xl:gap-8">
          <div className="relative overflow-hidden rounded-2xl border border-border bg-card lg:h-[760px]">
            <Image
              src="/optimized/feature2.webp"
              alt="AO desktop showing agent work moving toward review"
              fill
              priority
              sizes="(max-width: 1023px) 100vw, 58vw"
              className="object-cover opacity-55"
            />
            <div className="absolute inset-0 bg-gradient-to-br from-background via-background/80 to-background/30" />
            <Quote
              className="absolute -right-8 top-14 size-44 rotate-6 text-foreground/[0.04] sm:size-64"
              strokeWidth={1}
              aria-hidden="true"
            />

            <div className="relative flex flex-col justify-between p-5 sm:p-8 lg:h-full lg:p-10">
              <div className="max-w-3xl">
                <h1
                  className={`${testimonialDisplay.className} max-w-4xl text-4xl font-semibold leading-[1.02] text-foreground sm:text-5xl lg:text-6xl`}
                >
                  Put your AO experience into words.
                </h1>
                <p className="mt-5 max-w-2xl text-base leading-7 text-muted-foreground sm:text-lg">
                  Tell other builders what changed when you started orchestrating
                  coding agents with AO. We&apos;ll feature selected stories in the
                  testimonials section of our website.
                </p>
              </div>

              <div className="mt-10 border-t border-border/70 pt-6 lg:mt-auto">
                <div className="grid gap-x-6 gap-y-5 lg:grid-cols-2">
                  {testimonials.slice(1, 5).map((testimonial, index) => (
                    <blockquote
                      key={testimonial.author}
                      className={`${index > 0 ? "hidden lg:block" : ""} text-sm leading-6 text-foreground/80`}
                    >
                      <p>“{testimonial.quote}”</p>
                      <cite className="mt-2 block text-xs font-medium not-italic leading-5 text-muted-foreground">
                        {testimonial.author}
                      </cite>
                    </blockquote>
                  ))}
                </div>
                <a
                  href="#testimonial-examples"
                  className="mt-6 inline-flex text-sm font-medium text-foreground underline decoration-border underline-offset-4 transition-colors hover:decoration-foreground"
                >
                  More testimonials below ↓
                </a>
              </div>
            </div>
          </div>

          <aside className="flex w-full flex-col rounded-2xl border border-border bg-card p-6 sm:p-8 lg:min-h-[760px]">
            <div className="mb-6">
              <h2 className="text-2xl font-semibold text-foreground lg:text-3xl">
                Submit a testimonial
              </h2>
            </div>
            <TestimonialForm />
          </aside>
        </div>

        <section
          id="testimonial-examples"
          className="mx-auto mt-6 max-w-7xl scroll-mt-20 overflow-hidden rounded-2xl border border-border bg-card p-6 sm:p-8 lg:p-10"
        >
          <div className="border-b border-border pb-7">
            <h2
              className={`${testimonialDisplay.className} text-2xl font-semibold text-foreground sm:text-3xl`}
            >
              Some testimonials from existing users
            </h2>
          </div>

          <div className="grid sm:grid-cols-2">
            {testimonials.map((testimonial, index) => (
              <blockquote
                key={testimonial.author}
                className={`group relative border-border py-7 first:pt-8 ${
                  index > 0 ? "border-t" : ""
                } ${index < 2 ? "sm:border-t-0" : "sm:border-t"} ${
                  index === testimonials.length - 1 &&
                  testimonials.length % 2 === 1
                    ? "sm:col-span-2"
                    : index % 2 === 1
                      ? "sm:border-l sm:pl-8"
                      : "sm:pr-8"
                }`}
              >
                <div className="flex items-center justify-between gap-4">
                  <span className="font-mono text-[10px] uppercase tracking-[0.2em] text-muted-foreground">
                    Testimonial {String(index + 1).padStart(2, "0")}
                  </span>
                  <Quote
                    className="size-5 text-foreground/10 transition-colors group-hover:text-foreground/20"
                    aria-hidden="true"
                  />
                </div>
                <p className="mt-5 max-w-2xl text-base leading-7 text-foreground/90 sm:text-lg sm:leading-8">
                  “{testimonial.quote}”
                </p>
                <cite className="mt-4 block text-sm font-medium not-italic leading-6 text-muted-foreground">
                  {testimonial.author}
                </cite>
              </blockquote>
            ))}
          </div>
        </section>
      </section>
    </main>
  );
}
