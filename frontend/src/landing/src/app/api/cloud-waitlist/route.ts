import { createHash } from "node:crypto";
import { neon } from "@neondatabase/serverless";
import { type NextRequest, NextResponse } from "next/server";
import { z } from "zod";

const MAX_BODY_BYTES = 4096;
const RATE_LIMIT_WINDOW_MS = 60_000;
const RATE_LIMIT_MAX_REQUESTS = 60;

const submissionBuckets = new Map<string, { count: number; resetAt: number }>();

const waitlistSchema = z.object({
  email: z.string().trim().toLowerCase().email().max(254),
  role: z.string().trim().min(2).max(120),
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
      CREATE TABLE IF NOT EXISTS ao_cloud_waitlist (
        id BIGSERIAL PRIMARY KEY,
        email TEXT NOT NULL UNIQUE,
        role TEXT NOT NULL,
        source TEXT NOT NULL DEFAULT 'ao_cloud_waitlist',
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

  const rateLimitKey = getRateLimitKey(request);

  if (isRateLimited(rateLimitKey)) {
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

  const parsed = waitlistSchema.safeParse(body);

  if (!parsed.success) {
    return json({ ok: false, error: "Please enter a valid email and role." }, 400);
  }

  try {
    const databaseUrl = getDatabaseUrl();
    const sql = neon(databaseUrl);

    await ensureTable(databaseUrl);

    await sql`
      INSERT INTO ao_cloud_waitlist (email, role)
      VALUES (${parsed.data.email}, ${parsed.data.role})
      ON CONFLICT (email)
      DO UPDATE SET
        role = EXCLUDED.role,
        updated_at = now()
    `;

    return json({ ok: true });
  } catch {
    console.error("AO Cloud waitlist storage failed.");
    return json({ ok: false, error: "Unable to save waitlist request." }, 500);
  }
}
