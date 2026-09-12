import { neon } from "@neondatabase/serverless";

type Env = {
  DATABASE_URL: string;
};

const MAX_BODY_BYTES = 32_768;
const MAX_TESTIMONIAL_WORDS = 350;
const RATE_LIMIT_WINDOW_MS = 60_000;
const RATE_LIMIT_MAX_REQUESTS = 20;
const buckets = new Map<string, { count: number; resetAt: number }>();

let ensureTablePromise: Promise<void> | undefined;

function json(body: { ok: boolean; error?: string }, status = 200) {
  return Response.json(body, {
    status,
    headers: {
      "Cache-Control": "no-store",
      "Access-Control-Allow-Origin": "https://useao.dev",
      "Access-Control-Allow-Methods": "POST, OPTIONS",
      "Access-Control-Allow-Headers": "Content-Type",
    },
  });
}

async function hash(value: string) {
  const bytes = new TextEncoder().encode(value);
  const digest = await crypto.subtle.digest("SHA-256", bytes);
  return [...new Uint8Array(digest)]
    .map((byte) => byte.toString(16).padStart(2, "0"))
    .join("");
}

async function rateLimitKey(request: Request) {
  return hash(request.headers.get("cf-connecting-ip") || "unknown");
}

function isRateLimited(key: string) {
  const now = Date.now();
  const bucket = buckets.get(key);

  if (!bucket || bucket.resetAt <= now) {
    buckets.set(key, {
      count: 1,
      resetAt: now + RATE_LIMIT_WINDOW_MS,
    });
    return false;
  }

  bucket.count += 1;
  return bucket.count > RATE_LIMIT_MAX_REQUESTS;
}

function parseSingleLine(value: unknown, maxLength: number) {
  return typeof value === "string"
    ? value.replace(/[\r\n\0]/g, "").trim().slice(0, maxLength)
    : "";
}

function parseTestimonial(value: unknown) {
  return typeof value === "string"
    ? value.replace(/\0/g, "").trim().slice(0, 10_000)
    : "";
}

function countWords(value: string) {
  return value.trim() ? value.trim().split(/\s+/u).length : 0;
}

function validLinkedInProfileUrl(value: string) {
  try {
    const url = new URL(value);
    const hostname = url.hostname.toLowerCase().replace(/^www\./, "");
    return (
      url.protocol === "https:" &&
      hostname === "linkedin.com" &&
      /^\/in\/[^/]+\/?$/u.test(url.pathname)
    );
  } catch {
    return false;
  }
}

function validTweetUrl(value: string) {
  try {
    const url = new URL(value);
    const hostname = url.hostname.toLowerCase().replace(/^(?:www\.|mobile\.)/, "");
    return (
      url.protocol === "https:" &&
      (hostname === "x.com" || hostname === "twitter.com") &&
      /^\/[^/]+\/status\/\d+\/?$/u.test(url.pathname)
    );
  } catch {
    return false;
  }
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

export default {
  async fetch(request: Request, env: Env) {
    if (request.method === "OPTIONS") {
      return json({ ok: true });
    }

    if (request.method !== "POST") {
      return json({ ok: false, error: "Method not allowed." }, 405);
    }

    const contentType = request.headers.get("content-type") || "";

    if (!contentType.includes("application/json")) {
      return json({ ok: false, error: "Invalid request." }, 415);
    }

    const contentLength = Number(request.headers.get("content-length") || 0);

    if (contentLength > MAX_BODY_BYTES) {
      return json({ ok: false, error: "Request too large." }, 413);
    }

    if (isRateLimited(await rateLimitKey(request))) {
      return json({ ok: false, error: "Please try again in a minute." }, 429);
    }

    let body: Record<string, unknown>;

    try {
      const rawBody = await request.text();

      if (new TextEncoder().encode(rawBody).length > MAX_BODY_BYTES) {
        return json({ ok: false, error: "Request too large." }, 413);
      }

      const parsed = JSON.parse(rawBody);
      body = parsed && typeof parsed === "object" ? parsed : {};
    } catch {
      return json({ ok: false, error: "Invalid request." }, 400);
    }

    const testimonial = parseTestimonial(body.testimonial);
    const linkedinUrl = parseSingleLine(body.linkedinUrl, 500);
    const tweetUrl = parseSingleLine(body.tweetUrl, 500);
    const testimonialWords = countWords(testimonial);

    if (
      testimonial.length < 20 ||
      testimonialWords > MAX_TESTIMONIAL_WORDS ||
      !validLinkedInProfileUrl(linkedinUrl) ||
      (tweetUrl && !validTweetUrl(tweetUrl))
    ) {
      return json({ ok: false, error: "Please check your testimonial and profile links." }, 400);
    }

    try {
      const databaseUrl = env.DATABASE_URL.trim().replace(/^\uFEFF/, "");
      const sql = neon(databaseUrl);

      await ensureTable(databaseUrl);

      await sql`
        INSERT INTO ao_testimonial_submissions (testimonial, linkedin_url, tweet_url)
        VALUES (${testimonial}, ${linkedinUrl}, ${tweetUrl || null})
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
  },
};
