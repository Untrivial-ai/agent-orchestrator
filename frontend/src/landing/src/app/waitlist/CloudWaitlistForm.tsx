"use client";

import Link from "next/link";
import posthog from "posthog-js";
import { useId, useState } from "react";
import { track } from "@/lib/analytics";

export function CloudWaitlistForm() {
  const emailId = useId();
  const roleId = useId();
  const [email, setEmail] = useState("");
  const [role, setRole] = useState("");
  const [error, setError] = useState("");
  const [isSubmitting, setIsSubmitting] = useState(false);
  const [submitted, setSubmitted] = useState(false);

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    const normalizedEmail = email.trim().toLowerCase();
    const trimmedRole = role.trim();
    if (!normalizedEmail || !trimmedRole) return;
    setError("");
    setIsSubmitting(true);

    let wasOptedOut = false;
    try {
      wasOptedOut = posthog.has_opted_out_capturing();
      if (wasOptedOut) {
        posthog.opt_in_capturing();
      }
    } catch {
      wasOptedOut = false;
    }

    track("cloud_waitlist_signup", {
      email: normalizedEmail,
      role: trimmedRole,
    });

    try {
      if (wasOptedOut) {
        posthog.opt_out_capturing();
      }
    } catch {
      // The form should still complete if analytics is unavailable.
    }

    try {
      const response = await fetch("/api/cloud-waitlist/", {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
        },
        body: JSON.stringify({
          email: normalizedEmail,
          role: trimmedRole,
        }),
      });

      if (!response.ok) {
        throw new Error("Waitlist request failed");
      }

      setSubmitted(true);
    } catch {
      setError("We could not save your request. Please try again.");
    } finally {
      setIsSubmitting(false);
    }
  }

  if (submitted) {
    return (
      <div className="rounded-2xl border border-border bg-card p-6">
        <p className="text-xs font-medium uppercase tracking-[0.22em] text-muted-foreground">
          Request received
        </p>
        <h2 className="mt-3 text-2xl font-semibold text-foreground">
          You're on the AO Cloud waitlist.
        </h2>
        <p className="mt-3 text-sm leading-6 text-muted-foreground">
          Thanks. We'll use your response to prioritize early access for the
          first AO Cloud workspaces.
        </p>
      </div>
    );
  }

  return (
    <form onSubmit={handleSubmit} className="flex flex-1 flex-col gap-4">
      <div className="grid gap-2">
        <label htmlFor={emailId} className="text-sm font-medium text-foreground">
          Email
        </label>
        <input
          id={emailId}
          type="email"
          required
          autoComplete="email"
          maxLength={254}
          placeholder="you@company.com"
          value={email}
          onChange={(e) => setEmail(e.target.value)}
          className="min-h-12 w-full rounded-xl border border-border bg-background px-4 text-sm text-foreground placeholder:text-muted-foreground focus:outline-none focus:ring-2 focus:ring-ring"
        />
      </div>

      <div className="grid gap-2">
        <label htmlFor={roleId} className="text-sm font-medium text-foreground">
          Role at company
        </label>
        <input
          id={roleId}
          type="text"
          required
          autoComplete="organization-title"
          maxLength={120}
          placeholder="Engineering lead, founder, developer..."
          value={role}
          onChange={(e) => setRole(e.target.value)}
          className="min-h-12 w-full rounded-xl border border-border bg-background px-4 text-sm text-foreground placeholder:text-muted-foreground focus:outline-none focus:ring-2 focus:ring-ring"
        />
      </div>

      <button
        type="submit"
        disabled={isSubmitting}
        className="mt-2 inline-flex min-h-12 w-full items-center justify-center rounded-xl bg-foreground px-5 text-sm font-semibold text-background transition-opacity hover:opacity-90 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background disabled:cursor-not-allowed disabled:opacity-60"
      >
        {isSubmitting ? "Joining..." : "Join the waitlist"}
      </button>

      {error ? (
        <p className="text-sm leading-6 text-red-400" role="alert">
          {error}
        </p>
      ) : null}

      <p className="text-xs leading-relaxed text-muted-foreground">
        We'll only use this to contact you about AO Cloud. See our{" "}
        <Link className="underline underline-offset-2" href="/privacy/">
          privacy policy
        </Link>
        .
      </p>
    </form>
  );
}
