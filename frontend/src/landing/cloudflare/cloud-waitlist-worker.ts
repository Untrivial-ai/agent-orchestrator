import { neon } from "@neondatabase/serverless";

type Env = {
  DATABASE_URL: string;
};

const MAX_BODY_BYTES = 4096;
const RATE_LIMIT_WINDOW_MS = 60_000;
const RATE_LIMIT_MAX_REQUESTS = 60;
const buckets = new Map<string, { count: number; resetAt: number }>();

let ensureTablePromise: Promise<void> | undefined;

function json(body: { ok: boolean; error?: string }, status = 200) {
  return Response.json(body, {
    status,
    headers: {
      "Cache-Control": "no-store",
      "Access-Control-Allow-Origin": "https://orchestrator.inc",
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
  const forwardedFor = request.headers.get("cf-connecting-ip") || "unknown";
  return hash(forwardedFor);
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

function parseText(value: unknown, maxLength: number) {
  if (typeof value !== "string") {
    return "";
  }

  return value.replace(/[\r\n\0]/g, "").trim().slice(0, maxLength);
}

function parseEmail(value: unknown) {
  const email = parseText(value, 254).toLowerCase();

  if (!/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email)) {
    return "";
  }

  return email;
}

function isSocialProfile(value: string) {
  if (/^@[A-Za-z0-9_]{1,15}$/.test(value)) {
    return true;
  }

  try {
    const url = new URL(value.includes("://") ? value : `https://${value}`);
    const hostname = url.hostname.toLowerCase().replace(/^www\./, "");
    const pathParts = url.pathname.split("/").filter(Boolean);

    if (hostname === "linkedin.com" || hostname.endsWith(".linkedin.com")) {
      return pathParts.length >= 2 && pathParts[0]?.toLowerCase() === "in";
    }

    if (hostname === "x.com" || hostname === "twitter.com") {
      return pathParts.length === 1 && /^[A-Za-z0-9_]{1,15}$/.test(pathParts[0] || "");
    }
  } catch {
    return false;
  }

  return false;
}

async function ensureTable(databaseUrl: string) {
  if (!ensureTablePromise) {
    const sql = neon(databaseUrl);

    ensureTablePromise = sql`
      CREATE TABLE IF NOT EXISTS ao_cloud_waitlist (
        id BIGSERIAL PRIMARY KEY,
        email TEXT NOT NULL UNIQUE,
        role TEXT NOT NULL,
        social_profile TEXT,
        source TEXT NOT NULL DEFAULT 'ao_cloud_waitlist',
        created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
        updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
      )
    `.then(async () => {
      await sql`
        ALTER TABLE ao_cloud_waitlist
        ADD COLUMN IF NOT EXISTS social_profile TEXT
      `;
    });
  }

  return ensureTablePromise;
}

function getDatabaseUrl(env: Env) {
  return env.DATABASE_URL.trim().replace(/^\uFEFF/, "");
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

    const key = await rateLimitKey(request);

    if (isRateLimited(key)) {
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

    const email = parseEmail(body.email);
    const role = parseText(body.role, 120);
    const socialProfile = parseText(body.socialProfile, 300);

    if (!email || role.length < 2 || !isSocialProfile(socialProfile)) {
      return json(
        {
          ok: false,
          error: "Please enter a valid email, role, and LinkedIn or Twitter profile.",
        },
        400,
      );
    }

    try {
      const databaseUrl = getDatabaseUrl(env);
      const sql = neon(databaseUrl);
      await ensureTable(databaseUrl);

      await sql`
        INSERT INTO ao_cloud_waitlist (email, role, social_profile)
        VALUES (${email}, ${role}, ${socialProfile})
        ON CONFLICT (email)
        DO UPDATE SET
          role = EXCLUDED.role,
          social_profile = EXCLUDED.social_profile,
          updated_at = now()
      `;

      return json({ ok: true });
    } catch {
      console.error("AO Cloud waitlist storage failed.");
      return json({ ok: false, error: "Unable to save waitlist request." }, 500);
    }
  },
};
