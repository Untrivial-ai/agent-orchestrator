# Weekly changelog publishing

The public changelog is a curated weekly product update. Stable versions and
technical notes remain available in the release archive and on GitHub Releases.

The archive covers every completed week from the first repository commit. Its
first partial window is February 13–15, 2026; later windows run Monday through
Sunday. A hand-written release entry may occupy a weekly window, so the archive
does not publish a second generated entry for that same week.

## Automation boundary

The `Draft weekly changelog` workflow runs every Sunday and can also be started
manually. It collects merged pull requests since the most recent published
entry, removes pull requests already referenced by an older entry, and creates a
reviewable MDX draft on a deterministic `changelog/YYYY-MM-DD` branch.

The workflow may create or update a pull request. It cannot merge the pull
request, create a GitHub release, alter desktop artifacts, or publish the site
directly.

Pull request labels influence collection:

- `changelog:include` includes work that would otherwise look internal.
- `changelog:skip` excludes work that should not be announced.
- Conventional `feat:`, `fix:`, and `perf:` titles are classified automatically.
- `build:`, `chore:`, `ci:`, `docs:`, `refactor:`, and `test:` changes are skipped
  unless explicitly included.

## Human review

1. Confirm each included change is available to users.
2. Remove internal, reverted, duplicated, or feature-flagged work.
3. Pick two to four highlights and rewrite them around the user outcome.
4. Keep smaller improvements and fixes to one line each.
5. Add real product screenshots or a short recording when they make a feature
   easier to understand. Store media under `frontend/src/landing/public/changelog/`.
6. Check every pull request reference and contributor credit.
7. Remove the editorial checklist embedded in the generated MDX.
8. Preview desktop and mobile layouts, then wait for CI.
9. Merge the pull request when the copy and media are ready.

Merging to `main` triggers the existing landing deployment. That merge is the
publishing decision.

## Local checks

From `frontend/src/landing`:

```bash
npm run changelog:test
npm run changelog:validate
npm run build
```

To reconstruct missing historical weeks from the local Git history:

```bash
npm run changelog:backfill
```

The backfill preserves an existing entry for a covered week and creates a
deterministic `YYYY-MM-DD-weekly-update.mdx` file only for missing windows. The
validator rejects a gap or overlap between published ranges.

For a visual review, run the landing site and open it in the desktop browser
panel:

```bash
npm run dev
ao preview http://localhost:3000/changelog
```

## Content format

Weekly entries live under `frontend/src/landing/content/changelog/` and use MDX.
Required frontmatter:

```yaml
---
title: "A direct description of the week's most important change"
description: "One sentence covering the useful outcomes in this update."
date: "YYYY-MM-DD"
rangeStart: "YYYY-MM-DD"
rangeEnd: "YYYY-MM-DD"
image: "/changelog/YYYY-MM-DD-hero.webp" # optional
---
```

The homepage reads only these curated MDX entries. GitHub release bodies never
appear in the weekly feed or RSS feed.
