// Package onedev observes OneDev pull requests for AO's SCM integrations.
//
// This package is being built in slices. Slice 1 (this code) provides the
// foundation only: configuration, credential resolution, the HTTP client, an
// authenticated preflight, and repository-URL parsing. The seven
// scm.Provider observer methods and the daemon wiring land in Slice 2, so
// nothing here is registered with the observer yet.
//
// # Always self-hosted
//
// Unlike GitHub and GitLab there is no public OneDev SaaS instance — every
// deployment is self-hosted. There is therefore no default host and no
// default API base: the host allowlist (AO_ONEDEV_ALLOWED_HOSTS) is required
// configuration, and NewProvider fails with ErrNoAllowedHosts when it is
// empty rather than silently defaulting to some public endpoint.
//
// Allowlist entries may carry an explicit scheme because self-hosted OneDev
// is commonly reached over plain HTTP on a private network:
//
//	onedev.example.com           -> https://onedev.example.com/~api
//	onedev.example.com:6610      -> https://onedev.example.com:6610/~api
//	http://10.0.0.30:6610        -> http://10.0.0.30:6610/~api
//
// # API surface
//
// OneDev's REST root is "/~api", not "/api". Authentication is either a
// bearer access token or HTTP basic auth; both are modelled by Credential.
// GET /~api/projects is the preflight target — it is cheap, requires
// authentication, and returns 401 for a bad or missing credential.
//
// Listing endpoints paginate with offset/count query parameters. There is no
// Link header, and count is capped at 100 (a larger value is rejected with
// HTTP 406), so doGETPaginated walks pages until one comes back short.
//
// # No conditional requests
//
// OneDev returns neither ETag nor Last-Modified on API responses. This was
// verified against a live instance. The client therefore has no
// If-None-Match / If-Modified-Since plumbing at all — adding it would send
// headers the server ignores and invite callers to assume a 304 path that
// can never fire. A future guard for the observer must be synthesised from a
// cheap query (for example the newest pull-request update timestamp in a
// project) rather than from a transport-level validator; that logic belongs
// with the observer methods in Slice 2.
package onedev
