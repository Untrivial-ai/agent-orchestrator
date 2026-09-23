import { createHash } from "node:crypto";
import { neon } from "@neondatabase/serverless";
import { type NextRequest, NextResponse } from "next/server";
import { z } from "zod";
import {
  countWords,
  isLinkedInProfileUrl,
  isTweetUrl,
  MAX_TESTIMONIAL_WORDS,
} from "@/lib/testimonial-submission";

const MAX_BODY_BYTES = 32_768;
const RATE_LIMIT_WINDOW_MS = 60_000;
const RATE_LIMIT_MAX_REQUESTS = 20;

const submissionBuckets = new Map<string, { count: number; resetAt: number }>();

const testimonialSchema = z.object({
  testimonial: z
    .string()
    .trim()
    .min(20)
    .max(10_000)
    .refine((value) => countWords(value) <= MAX_TESTIMONIAL_WORDS),
  linkedinUrl: z.string().trim().max(500).refine(isLinkedInProfileUrl),
  tweetUrl: z
    .string()
    .trim()
    .max(500)
    .refine((value) => !value || isTweetUrl(value))
    .optional()
    .default(""),
});

let ensureTablePromise: Promise<void> | undefined;

function json(body: { ok: boolean; error?: string }, status = 200) {
  return NextResponse.json(body, {
    status,
    headers: {
      "Cache-Control": "no-store",
    },
  });
}

function getRateLimitKey(request: NextRequest) {
  const forwardedFor = request.headers.get("x-forwarded-for");
  const ip =
    forwardedFor?.split(",")[0]?.trim() ||
    request.headers.get("x-real-ip") ||
    "unknown";

  return createHash("sha256").update(ip).digest("hex");
}

function isRateLimited(key: string) {
  const now = Date.now();
  const bucket = submissionBuckets.get(key);

  if (!bucket || bucket.resetAt <= now) {
    submissionBuckets.set(key, {
      count: 1,
      resetAt: now + RATE_LIMIT_WINDOW_MS,
    });
    return false;
  }

  bucket.count += 1;
  return bucket.count > RATE_LIMIT_MAX_REQUESTS;
}

function getDatabaseUrl() {
  const databaseUrl = process.env.DATABASE_URL?.trim().replace(/^\uFEFF/, "");

  if (!databaseUrl) {
    throw new Error("DATABASE_URL is not configured");
  }

  return databaseUrl;
}

async function ensureTable(databaseUrl: string) {
  if (!ensureTablePromise) {
    const sql = neon(databaseUrl);

    ensureTablePromise = sql`
      CREATE TABLE IF NOT EXISTS ao_testimonial_submissions (
        id BIGSERIAL PRIMARY KEY,
        testimonial TEXT NOT NULL,
        linkedin_url TEXT NOT NULL UNIQUE,
        tweet_url TEXT,
        status TEXT NOT NULL DEFAULT 'pending',
        source TEXT NOT NULL DEFAULT 'ao_testimonial_submission',
        created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
        updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
      )
    `.then(() => undefined);
  }

  return ensureTablePromise;
}

export async function POST(request: NextRequest) {
  const contentType = request.headers.get("content-type") || "";

  if (!contentType.includes("application/json")) {
    return json({ ok: false, error: "Invalid request." }, 415);
  }

  const contentLength = Number(request.headers.get("content-length") || 0);

  if (contentLength > MAX_BODY_BYTES) {
    return json({ ok: false, error: "Request too large." }, 413);
  }

  if (isRateLimited(getRateLimitKey(request))) {
    return json({ ok: false, error: "Please try again in a minute." }, 429);
  }

  let body: unknown;

  try {
    const rawBody = await request.text();

    if (new TextEncoder().encode(rawBody).length > MAX_BODY_BYTES) {
      return json({ ok: false, error: "Request too large." }, 413);
    }

    body = JSON.parse(rawBody);
  } catch {
    return json({ ok: false, error: "Invalid request." }, 400);
  }

  const parsed = testimonialSchema.safeParse(body);

  if (!parsed.success) {
    return json({ ok: false, error: "Please check your testimonial and profile links." }, 400);
  }

  try {
    const databaseUrl = getDatabaseUrl();
    const sql = neon(databaseUrl);

    await ensureTable(databaseUrl);

    await sql`
      INSERT INTO ao_testimonial_submissions (testimonial, linkedin_url, tweet_url)
      VALUES (
        ${parsed.data.testimonial},
        ${parsed.data.linkedinUrl},
        ${parsed.data.tweetUrl || null}
      )
      ON CONFLICT (linkedin_url)
      DO UPDATE SET
        testimonial = EXCLUDED.testimonial,
        tweet_url = EXCLUDED.tweet_url,
        status = 'pending',
        updated_at = now()
    `;

    return json({ ok: true });
  } catch {
    console.error("AO testimonial storage failed.");
    return json({ ok: false, error: "Unable to save testimonial." }, 500);
  }
}
