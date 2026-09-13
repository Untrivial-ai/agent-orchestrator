# Landing deployment

The landing site is a Next.js static export served by **GitHub Pages**, with
Cloudflare providing DNS, HTTPS, Worker routes, and the old-domain redirects.
The public origin is `https://orchestrator.inc`.

`.github/workflows/deploy-landing.yml` builds `frontend/src/landing/out` and
deploys it on landing changes to `main`, every six hours, or a manual dispatch.
The schedule refreshes release/download data. The GitHub Pages custom domain
is a repository setting, not a Cloudflare Pages project or a build environment
variable. An Actions-based Pages deployment does not use a `CNAME` file.

## Domain configuration

- GitHub repository Settings → Pages → Custom domain: `orchestrator.inc`.
- Cloudflare `orchestrator.inc`: proxied apex A records pointing to GitHub Pages
  (`185.199.108.153`, `185.199.109.153`, `185.199.110.153`, `185.199.111.153`).
- `www.orchestrator.inc`: a redirect Worker custom domain that sends traffic to
  `https://orchestrator.inc`.
- Cloudflare `useao.dev`, `aoagents.dev`, and `ao-agents.com`: keep apex DNS
  proxied;
  `www.aoagents.dev` is a Worker custom domain with Cloudflare-managed DNS and
  TLS. The `www.ao-agents.com` alias is also a Worker custom domain. Redirect
  only the explicitly configured landing hostnames to
  `https://orchestrator.inc`, preserving the path and query
  string. Use a permanent **308** redirect to preserve the method/body for old
  form submissions as well as ordinary page navigation.
- Do not redirect other `aoagents.dev` subdomains: the cloud API, staging API,
  and other services have independent origins. Keep email DNS records and the
  Android application ID `aoagents.dev` unchanged.

The `ao-landing-domain-redirect` Worker implements these redirects. Its source
and Wrangler configuration live under `cloudflare/domain-redirect*`. It uses
an exact hostname allowlist and replaces only the scheme and host, preserving
the encoded path and query string. Deploy it separately from the static site:

```bash
wrangler deploy --config cloudflare/domain-redirect.wrangler.toml
```

The `useao.dev` and `www.useao.dev` records must be proxied for their Worker
routes to execute. DNS-only records bypass the Worker and send requests directly
to their configured origin. A successful `curl --resolve` check against
Cloudflare only verifies the staged edge configuration, not public readiness.
After enabling the proxy, verify public DNS and HTTP/HTTPS redirects without
`--resolve`.

Existing, more-specific old-domain API and pass routes remain active for
compatibility; the catch-all landing redirect does not replace those Workers.

These existing Workers must also have routes on the new domain; GitHub Pages
cannot execute the application's API handlers:

| Route | Worker |
| --- | --- |
| `orchestrator.inc/api/cloud-waitlist*` | `ao-cloud-waitlist` |
| `orchestrator.inc/api/testimonial-submissions*` | `ao-cloud-waitlist` |
| `orchestrator.inc/hackathons/syndicate/pass*` | `ao-syndicate-pass-router` |

The deployed `ao-cloud-waitlist` Worker handles both form routes. The two source
examples under `cloudflare/` are separate handlers; do not overwrite the
combined live Worker with just one of them. Browser forms use same-origin
relative URLs. New-domain forms therefore do not depend on the CORS response
header. Old cached pages continue posting to the retained, more-specific
old-domain Worker routes without a cross-origin redirect. If those routes are
later replaced by redirects, first make the combined Worker accept both
explicitly allowed origins during that transition.

## Cutover and verification

Add the domain to Cloudflare and change its Porkbun nameservers first. Prepare
DNS and Worker routes next. Change the GitHub Pages custom domain, verify HTTPS
on `orchestrator.inc`, then enable the old-domain redirects. Deploy the
updated static export so canonical metadata, sitemap, feeds, and public links
use the new origin.

```bash
cd frontend/src/landing
npm ci
npm run build
curl -I https://orchestrator.inc/
curl -I 'https://useao.dev/docs/installation/?utm_source=migration-check'
curl -I 'https://www.useao.dev/docs/installation/?utm_source=migration-check'
curl -I 'https://aoagents.dev/docs/installation/?utm_source=migration-check'
curl -I 'https://www.aoagents.dev/docs/installation/?utm_source=migration-check'
curl -I 'https://ao-agents.com/docs/installation/?utm_source=migration-check'
curl -I 'https://www.ao-agents.com/docs/installation/?utm_source=migration-check'
curl -I 'http://www.orchestrator.inc/docs/installation/?utm_source=migration-check'
curl -IL 'https://www.orchestrator.inc/docs/installation/?utm_source=migration-check'
curl -I https://orchestrator.inc/hackathons/syndicate/pass/
curl -i -X OPTIONS https://orchestrator.inc/api/cloud-waitlist/
curl -i -X OPTIONS https://orchestrator.inc/api/testimonial-submissions/
```

Expect the landing page and pass to return 200, old-domain requests to return
308 with the same path/query on `orchestrator.inc`, and API preflights to reach
the Workers. Verify `/sitemap.xml`, `/robots.txt`, and page canonical metadata
refer to `orchestrator.inc`. Check HTTP and HTTPS and the `www` variants without
following redirects first, then follow them to detect loops. Avoid submitting
real form data as a deployment check.

## Rollback

1. Remove the legacy-domain redirect Worker routes, then restore the GitHub Pages
   custom domain to `useao.dev` and verify the apex returns 200. Keep its
   original proxied apex records and all more-specific API/pass routes.
2. Remove the `www.orchestrator.inc` Worker custom domain only after the restored
   `useao.dev` origin is healthy. Cloudflare manages custom-domain DNS and TLS;
   detaching one does not guarantee replacement DNS or TLS is ready.
3. Keep `orchestrator.inc` serving during a full rollback for clients that
   cached permanent redirects. Do not add a reverse permanent redirect that
   could create a loop. These rollback operations have not been exercised
   against production.

References: [GitHub Pages custom domains](https://docs.github.com/en/pages/configuring-a-custom-domain-for-your-github-pages-site/managing-a-custom-domain-for-your-github-pages-site),
[Cloudflare Worker routes](https://developers.cloudflare.com/workers/configuration/routing/routes/),
[Cloudflare Worker custom domains](https://developers.cloudflare.com/workers/configuration/routing/custom-domains/).
