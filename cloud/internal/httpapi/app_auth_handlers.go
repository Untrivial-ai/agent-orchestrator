package httpapi

import "net/http"

// appAuthReturnHTML is the HTTPS landing page WorkOS redirects the browser to
// after a desktop sign-in (the packaged app sets redirect_uri =
// https://<cp-host>/app/auth/return). Serving a real HTTPS page here — rather
// than letting WorkOS redirect the browser straight to the ao-app:// custom
// scheme — does two things:
//
//  1. The browser lands on a loadable page instead of hanging indefinitely on
//     an unresolvable custom-scheme navigation (the "stuck loading tab").
//  2. The OS "open Agent Orchestrator?" prompt is attributed to this AO-owned
//     origin rather than the opaque WorkOS AuthKit subdomain.
//
// The page does no server-side processing of the query: the desktop app performs
// the PKCE code exchange. The OAuth result (code/state, or error) is read and
// forwarded to the ao-app:// deep link entirely client-side, and is never
// reflected into the HTML, so there is nothing to inject.
const appAuthReturnHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex">
<title>Signing in to Agent Orchestrator</title>
<style>
  :root { color-scheme: light dark; }
  body { font: 15px -apple-system, BlinkMacSystemFont, "Segoe UI", system-ui, sans-serif;
         max-width: 30rem; margin: 18vh auto; padding: 0 1.5rem; text-align: center;
         color: #1a1a1a; background: #ffffff; }
  @media (prefers-color-scheme: dark) { body { color: #ededed; background: #0b0b0c; } }
  h1 { font-size: 1.2rem; font-weight: 600; margin: 0 0 .5rem; }
  p { color: #6b7280; margin: 0 0 1.5rem; }
  a.button { display: inline-block; padding: .6rem 1.2rem; border-radius: .5rem;
             background: #3b82f6; color: #ffffff; text-decoration: none; font-weight: 500; }
</style>
</head>
<body>
<h1>You&rsquo;re signed in</h1>
<p>Returning you to Agent Orchestrator. You can close this tab.</p>
<a class="button" id="return" href="ao-app://callback">Return to Agent Orchestrator</a>
<script>
  (function () {
    var target = "ao-app://callback" + (window.location.search || "");
    document.getElementById("return").setAttribute("href", target);
    // Hand the sign-in result back to the desktop app.
    window.location.replace(target);
  })();
</script>
</body>
</html>
`

// appAuthReturn serves the desktop sign-in bounce page. It is a public,
// unauthenticated route: the browser reaches it with only the WorkOS OAuth query
// (no bearer token), and the only action it takes is handing that query to the
// ao-app:// deep link.
func (s *Server) appAuthReturn(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(appAuthReturnHTML))
}
