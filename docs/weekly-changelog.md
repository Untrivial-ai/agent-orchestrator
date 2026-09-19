# Weekly changelog ritual

AO ships a lot of surface area every week (hundreds of merged PRs in a busy
month). Versioned GitHub Releases remain the install/upgrade source of truth.
The **weekly changelog** on the landing site is the discoverability layer: a
short, media-backed narrative in the style of Superset's "What's New", so
momentum is visible without reading the full release notes.

Published entries live in
[`frontend/src/landing/content/changelog/`](../frontend/src/landing/content/changelog/)
as MDX with `kind: weekly`. The index at `/changelog` already says "Updated
weekly"; this doc is the operator checklist that keeps that promise.

## Cadence

- **When:** once per week (Friday is the default; any consistent day works).
- **Window:** every PR merged to `main` since the previous weekly entry
  (inclusive lower bound is the day after that entry's `date`).
- **Owner:** whoever is on release/comms rotation that week. One person drafts;
  a second person spot-checks Feature copy and media.

## Draft

From the repo root (requires authenticated `gh`):

```bash
node scripts/draft-weekly-changelog.mjs \
  --out frontend/src/landing/content/changelog/draft-weekly.mdx
```

The script:

1. Finds the latest MDX entry with `kind: weekly` (or a `YYYY-MM-DD-…` slug).
2. Lists PRs merged to `main` since that date via the GitHub GraphQL API.
3. Groups them into **Features**, **Improvements**, and **Fixes** from
   conventional-commit titles (with a small imperative-verb heuristic).
4. Emits one `<MediaPlaceholder />` per feature for a human to record into.
5. Leaves `draft: true` so an unfinished file never publishes if committed by
   mistake.

Override the window with `--since YYYY-MM-DD` when backfilling. Pure helpers are
covered by `node --test scripts/draft-weekly-changelog.test.mjs`.

## Curate

Do not publish the raw draft. Edit it into a readable entry:

1. **Rename the file** to `YYYY-MM-DD-<short-slug>.mdx` (match the week-ending
   date and a few memorable words).
2. **Collapse Features** into a handful of narrative sections (Superset-style
   H2/H3 with PR links). Move the long tail under "Also shipped".
3. **Trim Improvements and Fixes** to the user-visible set; link the rest from
   GitHub compare if needed.
4. **Attribute contributors** with `@login` from the draft's Contributors block.
5. **Record media** for each Feature slot (GIF for interaction, screenshot for
   a static surface). Save under
   `frontend/src/landing/public/changelog/<slug>/`, then replace each
   `<MediaPlaceholder />` with a normal image or `<Video src="…" />`.
6. Set `draft: false` (or remove the field) before merging.

Frontmatter shape:

```yaml
---
title: "Short headline of the week's story"
description: "One or two sentences for OG/RSS."
date: "YYYY-MM-DD"
kind: weekly
---
```

`MediaPlaceholder` renders as a dashed orange callout on the site so unfinished
recording work is obvious in preview. It must not ship in the final entry once
assets exist.

## Publish

1. Open a PR that adds only the new MDX (and media assets). Keep it separate
   from product code unless the ritual PR is intentionally bundling the first
   pipeline (script + format) as well.
2. Smoke the landing build: `npm --prefix frontend/src/landing run build`.
3. After merge, confirm `/changelog` and `/changelog/<slug>/` on the deployed
   site. The RSS feed at `/changelog.xml` picks curated MDX up automatically.

## Relationship to GitHub Releases

| Surface | Job |
| --- | --- |
| Weekly MDX on `/changelog` | Narrative momentum, media, contributor shout-outs |
| GitHub Releases + curated version MDX (e.g. `0.13.0`) | Install artifacts, upgrade notes, full cycle rollup |

A busy week can ship a weekly entry **and** fold into the next versioned
release entry later. Do not delete weekly entries when a version rollup lands;
the index sorts by `date` and keeps both.

## Known gaps / ask rather than guess

- Exact publish day and primary owner (comms vs eng) if rotation is unclear —
  pick one in the PR that establishes the ritual and stick to it.
- Whether Cloud-only or flag-gated work should get Feature media when most
  desktop users cannot click through yet — default is yes when the surface is
  real, with copy that says it is flag-gated.
