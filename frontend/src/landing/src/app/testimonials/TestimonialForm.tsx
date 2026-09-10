"use client";

import { ExternalLink } from "lucide-react";
import { useId, useState } from "react";
import {
  countWords,
  isLinkedInProfileUrl,
  isTweetUrl,
  keepWithinWordLimit,
} from "@/lib/testimonial-submission";

const TWEET_INTENT_URL =
  "https://twitter.com/intent/tweet?text=I%27ve%20been%20using%20%40aoagents%20to%20run%20coding%20agents%20in%20parallel.%20Here%27s%20what%20I%20think%3A&url=https%3A%2F%2Fuseao.dev";

export function TestimonialForm() {
  const testimonialId = useId();
  const testimonialHelpId = useId();
  const linkedinId = useId();
  const tweetId = useId();
  const [testimonial, setTestimonial] = useState("");
  const [linkedinUrl, setLinkedinUrl] = useState("");
  const [tweetUrl, setTweetUrl] = useState("");
  const [error, setError] = useState("");
  const [isSubmitting, setIsSubmitting] = useState(false);
  const [submitted, setSubmitted] = useState(false);
  const wordCount = countWords(testimonial);

  async function handleSubmit(event: React.FormEvent) {
    event.preventDefault();

    const trimmedTestimonial = testimonial.trim();
    const trimmedLinkedinUrl = linkedinUrl.trim();
    const trimmedTweetUrl = tweetUrl.trim();

    if (trimmedTestimonial.length < 20) {
      setError("Please add a little more detail to your testimonial.");
      return;
    }
    if (!isLinkedInProfileUrl(trimmedLinkedinUrl)) {
      setError("Please enter a valid LinkedIn profile URL.");
      return;
    }
    if (trimmedTweetUrl && !isTweetUrl(trimmedTweetUrl)) {
      setError("Please enter a direct link to your post on X.");
      return;
    }

    setError("");
    setIsSubmitting(true);

    try {
      const response = await fetch("/api/testimonial-submissions/", {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
        },
        body: JSON.stringify({
          testimonial: trimmedTestimonial,
          linkedinUrl: trimmedLinkedinUrl,
          tweetUrl: trimmedTweetUrl,
        }),
      });

      if (!response.ok) {
        throw new Error("Testimonial submission failed");
      }

      setSubmitted(true);
    } catch {
      setError("We could not save your testimonial. Please try again.");
    } finally {
      setIsSubmitting(false);
    }
  }

  if (submitted) {
    return (
      <div className="flex flex-1 flex-col justify-center rounded-2xl border border-border bg-background/60 p-6">
        <p className="text-xs font-medium uppercase tracking-[0.22em] text-muted-foreground">
          Testimonial received
        </p>
        <h2 className="mt-3 text-2xl font-semibold text-foreground">
          Thank you for sharing your AO story.
        </h2>
        <p className="mt-3 text-sm leading-6 text-muted-foreground">
          We&apos;ll review it for the testimonials section of our website and
          use your LinkedIn profile for the accompanying attribution.
        </p>
      </div>
    );
  }

  return (
    <form onSubmit={handleSubmit} className="flex flex-1 flex-col gap-4">
      <div className="grid gap-2">
        <div className="flex items-end justify-between gap-4">
          <label
            htmlFor={testimonialId}
            className="text-sm font-medium text-foreground lg:text-base"
          >
            Your testimonial
          </label>
          {wordCount > 0 ? (
            <span className="text-xs tabular-nums text-muted-foreground lg:text-sm">
              {wordCount} {wordCount === 1 ? "word" : "words"}
            </span>
          ) : null}
        </div>
        <textarea
          id={testimonialId}
          required
          rows={8}
          maxLength={10_000}
          aria-describedby={testimonialHelpId}
          placeholder="What changed in the way you work after using AO? A specific outcome or moment is especially helpful."
          value={testimonial}
          onChange={(event) =>
            setTestimonial((currentValue) =>
              keepWithinWordLimit(event.target.value, currentValue),
            )
          }
          className="min-h-44 w-full resize-y rounded-xl border border-border bg-background px-4 py-3 text-sm leading-6 text-foreground placeholder:text-muted-foreground/50 focus:outline-none focus:ring-2 focus:ring-ring lg:text-base lg:leading-7"
        />
        <p id={testimonialHelpId} className="text-xs leading-5 text-muted-foreground lg:text-sm lg:leading-6">
          30–100 words recommended. Concrete details make the strongest stories.
        </p>
      </div>

      <div className="grid gap-2">
        <label
          htmlFor={linkedinId}
          className="text-sm font-medium text-foreground lg:text-base"
        >
          LinkedIn profile
        </label>
        <input
          id={linkedinId}
          type="url"
          inputMode="url"
          required
          autoComplete="url"
          maxLength={500}
          placeholder="https://www.linkedin.com/in/your-name"
          value={linkedinUrl}
          onChange={(event) => setLinkedinUrl(event.target.value)}
          className="min-h-12 w-full rounded-xl border border-border bg-background px-4 text-sm text-foreground placeholder:text-muted-foreground/50 focus:outline-none focus:ring-2 focus:ring-ring lg:text-base"
        />
      </div>

      <div className="grid gap-2">
        <div className="flex items-center justify-between gap-4">
          <label htmlFor={tweetId} className="text-sm font-medium text-foreground lg:text-base">
            Tweet about AO <span className="font-normal text-muted-foreground">(optional)</span>
          </label>
          <a
            href={TWEET_INTENT_URL}
            target="_blank"
            rel="noreferrer"
            className="inline-flex items-center gap-1 text-xs font-medium text-foreground underline decoration-border underline-offset-4 transition-colors hover:decoration-foreground lg:text-sm"
          >
            Draft a tweet
            <ExternalLink className="size-3" aria-hidden="true" />
          </a>
        </div>
        <input
          id={tweetId}
          type="url"
          inputMode="url"
          maxLength={500}
          placeholder="https://x.com/your-name/status/..."
          value={tweetUrl}
          onChange={(event) => setTweetUrl(event.target.value)}
          className="min-h-12 w-full rounded-xl border border-border bg-background px-4 text-sm text-foreground placeholder:text-muted-foreground/50 focus:outline-none focus:ring-2 focus:ring-ring lg:text-base"
        />
        <p className="text-xs leading-5 text-muted-foreground lg:text-sm lg:leading-6">
          Tweet your experience, then paste the link so we can embed it directly.
        </p>
      </div>

      <button
        type="submit"
        disabled={isSubmitting}
        className="mt-1 inline-flex min-h-12 w-full items-center justify-center rounded-xl bg-foreground px-5 text-sm font-semibold text-background transition-opacity hover:opacity-90 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background disabled:cursor-not-allowed disabled:opacity-60 lg:text-base"
      >
        {isSubmitting ? "Submitting..." : "Submit testimonial"}
      </button>

      {error ? (
        <p className="text-sm leading-6 text-red-400" role="alert">
          {error}
        </p>
      ) : null}

    </form>
  );
}
