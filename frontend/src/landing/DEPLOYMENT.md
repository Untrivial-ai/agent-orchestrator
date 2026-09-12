# Landing deployment

The landing site is a Next.js static export served by **GitHub Pages**, with
Cloudflare providing DNS, HTTPS, Worker routes, and the old-domain redirect.
The public origin is `https://useao.dev`.

`.github/workflows/deploy-landing.yml` builds `frontend/src/landing/out` and
deploys it on landing changes to `main`, every six hours, or a manual dispatch.
The schedule refreshes release/download data. The GitHub Pages custom domain
is a repository setting, not a Cloudflare Pages project or a build environment
variable. An Actions-based Pages deployment does not use a `CNAME` file.

## Domain configuration

- GitHub repository Settings → Pages → Custom domain: `useao.dev`.
- Cloudflare `useao.dev`: proxied apex A records pointing to GitHub Pages
  (`185.199.108.153`, `185.199.109.153`, `185.199.110.153`, `185.199.111.153`).
- `www.useao.dev`: proxied CNAME to `untrivial-ai.github.io`, redirecting to
  `https://useao.dev`.
- Cloudflare `aoagents.dev`: keep apex DNS proxied; `www` is a Worker custom
  domain with Cloudflare-managed DNS and TLS. Redirect only
  those two hostnames to `https://useao.dev`, preserving the path and query
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

The `www.useao.dev` CNAME must be proxied for its Worker route to execute.
While it is DNS-only, that route is inactive: public DNS resolves directly to
GitHub Pages and requests receive GitHub's own redirects/errors. The pre-proxy
public check returned an HTTP 301 to `http://useao.dev` and an HTTPS certificate
hostname mismatch. A successful `curl --resolve` check against Cloudflare only
verifies the staged edge configuration, not public readiness. After enabling
the proxy, verify public DNS and HTTP/HTTPS redirects without `--resolve`.

Existing, more-specific old-domain API and pass routes remain active for
compatibility; the catch-all landing redirect does not replace those Workers.

These existing Workers must also have routes on the new domain; GitHub Pages
cannot execute the application's API handlers:

| Route | Worker |
| --- | --- |
| `useao.dev/api/cloud-waitlist*` | `ao-cloud-waitlist` |
| `useao.dev/api/testimonial-submissions*` | `ao-cloud-waitlist` |
| `useao.dev/hackathons/syndicate/pass*` | `ao-syndicate-pass-router` |

The deployed `ao-cloud-waitlist` Worker handles both form routes. The two
source examples under `cloudflare/` are separate handlers; do not overwrite
the combined live Worker with just one of them. Browser forms use same-origin
relative URLs. The combined live deployment still sends its existing
`Access-Control-Allow-Origin: https://aoagents.dev` header; the source examples
are not its deployed artifact. New-domain forms are same-origin and do not
depend on that CORS header. Old cached pages continue posting to the retained,
more-specific old-domain Worker routes without a cross-origin redirect.
If those routes are later replaced by redirects, first make the combined
Worker accept both explicitly allowed origins during that transition.

## Cutover and verification

Prepare DNS and Worker routes first. Change the GitHub Pages custom domain,
verify HTTPS on `useao.dev`, then enable the old-domain redirect. Deploy the
updated static export so canonical metadata, sitemap, feeds, and public links
use the new origin.

```bash
cd frontend/src/landing
npm ci
npm run build
curl -I https://useao.dev/
curl -I 'https://aoagents.dev/docs/installation/?utm_source=migration-check'
curl -I 'https://www.aoagents.dev/docs/installation/?utm_source=migration-check'
curl -I 'http://www.useao.dev/docs/installation/?utm_source=migration-check'
curl -IL 'https://www.useao.dev/docs/installation/?utm_source=migration-check'
curl -I https://useao.dev/hackathons/syndicate/pass/
curl -i -X OPTIONS https://useao.dev/api/cloud-waitlist/
curl -i -X OPTIONS https://useao.dev/api/testimonial-submissions/
```

Expect the landing page and pass to return 200, old-domain requests to return
308 with the same path/query on `useao.dev`, and API preflights to reach the
Workers. Verify `/sitemap.xml`, `/robots.txt`, and page canonical metadata refer
to `useao.dev`. Check HTTP and HTTPS and the `www` variants without following
redirects first, then follow them to detect loops. Avoid submitting real form
data as a deployment check.

## Rollback

1. Remove the `aoagents.dev/*` redirect Worker route, then restore the GitHub
   Pages custom domain to `aoagents.dev` and verify the apex returns 200.
   Keep the original apex DNS and all six more-specific API/pass routes.
2. Handle `www.aoagents.dev` separately: deleting the apex route does not
   detach its Worker **Custom Domain**. To return this hostname to Pages,
   remove the Custom Domain under the Worker's Settings → Domains & Routes,
   remove its `custom_domain = true` entry from the deployment config, inspect
   the resulting DNS, and establish a proxied `www` CNAME to
   `untrivial-ai.github.io`. There was no working pre-migration `www` hostname
   to restore. Cloudflare manages the Custom Domain's DNS and certificate;
   detaching it does not guarantee that replacement DNS/TLS is ready.
   Its Advanced Certificate is **not automatically deleted** on detach.
   Verify public DNS and HTTPS, allow any necessary certificate provisioning,
   and only then consider removing an unused certificate.
3. Keep `useao.dev` serving content during a full rollback for clients that
   cached the permanent redirect, for example by proxying to the restored
   origin. Do not add a reverse permanent redirect that could create a loop.
   Verify both `www` destinations before repointing them. These rollback
   operations have not been exercised against production.

References: [GitHub Pages custom domains](https://docs.github.com/en/pages/configuring-a-custom-domain-for-your-github-pages-site/managing-a-custom-domain-for-your-github-pages-site),
[Cloudflare Worker routes](https://developers.cloudflare.com/workers/configuration/routing/routes/),
[Cloudflare Worker custom domains](https://developers.cloudflare.com/workers/configuration/routing/custom-domains/).
