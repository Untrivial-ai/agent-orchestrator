# Launch analytics (Product Hunt)

Self-contained layer for the Product Hunt launch. It adds the pieces that were
missing and reuses everything the site already tracks. It touches exactly one
existing file (`app/layout.tsx`, one mount line), so it merges cleanly and can
be removed after the launch without unpicking anything.

## What this adds

- **`utm.ts`** — the canonical UTM link registry. One tagged inbound link per
  channel, all pointing at `aoagents.dev`, so PostHog attributes each visit to
  the right `utm_source`. `buildUtmUrl` + `LAUNCH_CHANNELS`.
- **`context.ts`** — normalizes the visit into `source` / `campaign` /
  `user_type` / `device`. Pure and unit-tested. `campaign` is the visit's own
  `utm_campaign` when it carried one and is **absent otherwise** — direct and
  untagged traffic is never relabeled `launch_day`.
- **`events.ts`** — the launch event names (`ph_referral_visit`,
  `ph_badge_click`, `ph_upvote_cta_click`, `return_visit`), fired through the
  shared `track()` wrapper at their call sites. (A `ph_comment_cta_click`
  event is added when a comment CTA actually exists — not before.)
- **Super-properties** — `source` / `campaign` / `device` are registered from
  the PostHog init `loaded` callback in `instrumentation-client.ts`, **before**
  the consent opt-in. That ordering is load-bearing: for an already-consented
  visitor, `opt_in_capturing()` emits the initial pageview synchronously inside
  `loaded`, so registering any later (e.g. from a React effect) would send that
  first pageview unattributed. From init on, **every** event, including
  autocaptured pageviews and the existing `download_clicked` /
  `waitlist_signup` / `section_viewed`, can be broken down by launch source
  **without editing those call sites**. UTMs are re-read from campaign.ts
  (capture-once, persisted for the tab session) on every load, so a visitor
  who arrived tagged keeps their attribution across an untagged reload;
  same-site referrers are ignored. An untagged external arrival — whose link
  carried no utm_* — has its referrer-inferred source remembered in
  sessionStorage for the tab session, so it too survives a same-site reload.
  A visit without `utm_campaign` has `campaign` unregistered, so a stale value
  cannot persist in the cookie.
- **`LaunchAnalytics`** (`app/components/LaunchAnalytics`) — mounted once in the
  layout; thin wiring only. Fires `ph_referral_visit` (once per tab session,
  Product Hunt traffic only) and `return_visit` (a **new** tab session of a
  browser seen before — a reload of a first-ever visit does not count, and if
  sessionStorage is blocked the return signal is suppressed rather than
  guessed at). Firing is consent-aware: nothing is captured — and no once-only
  flag is consumed — until the visitor accepts analytics, so a first-time PH
  visitor who accepts the banner a minute in is still counted. All decisions
  and guarded storage/consent handling live in `launch/visit.ts` (injectable,
  unit-tested with fakes and fake timers), the registration decision in
  `launch/registration.ts`, and the shared browser-input path in
  `launchContextFromBrowser` — the same single-caller seam shape as
  `marketing-consent.ts` and `campaign.ts`.
- **`ProductHuntBadge`** (`app/components/ProductHuntBadge`) — a drop-in
  Product Hunt CTA with `intent="badge" | "upvote"` selecting the event fired
  (`ph_badge_click` / `ph_upvote_cta_click`). The header mounts the `upvote`
  variant for launch day; remove it after.

## What already exists (not duplicated)

- **Autocaptured by PostHog, no code needed:** page views, sessions, session
  duration, bounce, referrer, country, device, browser, unique visitors.
- **`campaign.ts`:** captures `utm_source/medium/campaign/term/content` on entry
  and persists them across the tab. This is the referral backbone; the launch
  layer normalizes on top of it.
- **Already fired elsewhere:** `download_clicked`, `waitlist_signup`,
  `install_command_copied`, `section_viewed`, `video_started`, `video_progress`,
  `outbound_link_clicked`. These now inherit the launch super-properties for
  free.

## Deliberately out of scope (app-side, not the marketing site)

These from the launch spec happen **inside the product**, so they are emitted by
the desktop app / daemon, not the landing page, and must not be faked here:
`signup_completed`, `login_completed`, `workspace_created`, `agent_created`,
`first_task_run`, `orchestrator_created`, `subagent_added`, `workflow_*`,
`*_connected`, `browser_agent_used`, `handoff_created`,
`multi_agent_task_completed`, `api_key_created`, `integration_connected`. The
app already emits its own usage events (e.g. `ao.session.spawned`); the funnel
below joins to those in the same PostHog project.

## The funnel

`Product Hunt visit → CTA click → signup → workspace created → first agent →
first successful orchestration`

- **Steps 1–2 (this layer):** `ph_referral_visit` / `source = product_hunt`,
  then the existing CTA events (`download_clicked`, `waitlist_signup`,
  `ph_upvote_cta_click`) now carry `source`.
- **Steps 3–6 (app-side):** the desktop app's own events. Attribution across the
  marketing→app boundary is only as good as what the app captures; the marketing
  site cannot set those. Treat the landing funnel (visit → CTA) as the launch
  scorecard and the app funnel (spawn → success) separately unless/until the app
  also records `utm_source`.

## Caveat that affects the numbers

The site is **opt-out by default** behind cookie consent
(`opt_out_capturing_by_default: true`). PostHog only records events for visitors
who **accept** analytics. So every count here is "of consenting visitors" and
undercounts total Product Hunt traffic. Read rates and trends, not absolutes.
(The launch once-only events wait for consent rather than being silently
dropped, but a visitor who never accepts is not counted at all.) Note also that
**unique visitors** are unreliable here: the project runs with
`person_profiles: "never"`, so there is no person record to dedupe against —
trust session counts instead.

## Canonical UTM links

Generated by `utm.ts` (`LAUNCH_CHANNELS`). Paste each `link` on its channel:

| Channel | utm_source | Link |
|---|---|---|
| Product Hunt | `product_hunt` | `https://aoagents.dev/?utm_source=product_hunt&utm_medium=referral&utm_campaign=launch_day` |
| X / Twitter | `x` | `https://aoagents.dev/?utm_source=x&utm_medium=social&utm_campaign=launch_day` |
| LinkedIn | `linkedin` | `https://aoagents.dev/?utm_source=linkedin&utm_medium=social&utm_campaign=launch_day` |
| YouTube | `youtube` | `https://aoagents.dev/?utm_source=youtube&utm_medium=social&utm_campaign=launch_day` (profile: `https://www.youtube.com/@itrytoohard`) |
| Discord | `discord` | `https://aoagents.dev/?utm_source=discord&utm_medium=community&utm_campaign=launch_day` |
| GitHub | `github` | `https://aoagents.dev/?utm_source=github&utm_medium=referral&utm_campaign=launch_day` |
| Instagram (TODO) | `instagram` | `https://aoagents.dev/?utm_source=instagram&utm_medium=social&utm_campaign=launch_day` |

Instagram has no account yet: the link is ready, the profile URL is a
placeholder to fill in when the handle exists.

## Testing locally

1. **Unit tests** (pure logic): the landing tests run under the frontend vitest
   config —
   `cd frontend && npx vitest run src/landing/src/lib/analytics/launch`.
2. **End to end in the browser:**
   - `cd frontend/src/landing && npm run dev`, open
     `http://localhost:3000/?utm_source=product_hunt&utm_medium=referral&utm_campaign=launch_day`.
   - Accept the cookie banner (otherwise capture stays opted out by design).
   - In the console, `posthog.debug()` then reload; watch the network calls to
     `/ingest`. You should see `ph_referral_visit`, and on every event the
     `source: "product_hunt"`, `campaign: "launch_day"`, and `device`
     super-properties.
   - Click a download / waitlist CTA and confirm those existing events also now
     carry `source`.
   - Set `NEXT_PUBLIC_POSTHOG_KEY` in `.env.local` to a dev project key first, or
     the client stays uninitialized and nothing sends (by design).
