// Package webui serves the embedded web UI from the LAN listener.
package webui

import (
	"bytes"
	"embed"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed all:dist
var distFS embed.FS

// shadowedPrefixes are the paths the static handler must never serve — they are
// reserved for the API, terminal, health, and control surface. The router's
// NotFound hook (mountWebUI in router.go) checks IsAPIShapedPath before ever
// calling this handler, but the check is repeated here as defense-in-depth: a
// future caller that mounts Handler() directly, without that gate, still can't
// have it swallow one of these routes into an index.html response.
var shadowedPrefixes = []string{
	"/api",
	"/mux",
	"/healthz",
	"/readyz",
	"/shutdown",
	"/internal/",
	"/login",
}

// IsAPIShapedPath reports whether path belongs to the API/control surface the
// static handler must never shadow. Exported so router.go's NotFound hook can
// keep returning the JSON envelope for these paths instead of falling back to
// the SPA's index.html. Matching is on an exact segment boundary — "/api"
// blocks itself and everything beneath it ("/api/v1/sessions") but must not
// catch an unrelated sibling like "/apix" or "/loginpage" (mirrors
// lan_listener.go's isLANControlBlockedPath).
func IsAPIShapedPath(path string) bool {
	for _, prefix := range shadowedPrefixes {
		trimmed := strings.TrimSuffix(prefix, "/")
		if path == trimmed || strings.HasPrefix(path, trimmed+"/") {
			return true
		}
	}
	return false
}

// Handler serves the embedded SPA with history fallback. It is mounted behind
// the router's NotFound hook (which already excludes API-shaped paths via
// IsAPIShapedPath) and, on the LAN listener, behind authMiddleware — so every
// request reaching this handler is already known to be a legitimate,
// authenticated (or /login) static-asset request.
func Handler() http.Handler {
	// Prepare the embedded filesystem. Strip the "dist/" prefix since it's already
	// part of the embed directive.
	distDir, err := fs.Sub(distFS, "dist")
	if err != nil {
		panic(fmt.Sprintf("failed to prepare embedded dist: %v", err))
	}

	fileServer := http.FileServer(http.FS(distDir))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Defense-in-depth: never serve a shadowed path even if reached directly.
		if IsAPIShapedPath(r.URL.Path) {
			http.NotFound(w, r)
			return
		}

		// "/" and "/index.html" always serve the app shell directly — never
		// through http.FileServer. Go's http.FileServer has a documented quirk:
		// a request that resolves to a file literally named "index.html" gets
		// redirected to the containing directory ("/index.html" -> "/") to avoid
		// duplicate URLs for the same content. Handling these two paths here,
		// up front, means we never hand FileServer a request whose path is
		// literally "index.html" (whether the original request or a rewritten
		// SPA-fallback path), so that redirect never fires.
		trimmed := strings.TrimPrefix(r.URL.Path, "/")
		if trimmed == "" || trimmed == "index.html" {
			if serveIndex(w, r, distDir) {
				return
			}
			http.NotFound(w, r) // no embedded index.html (shouldn't happen post-build)
			return
		}

		// Try to serve the requested file as-is.
		if f, err := distDir.Open(trimmed); err == nil {
			_ = f.Close()
			fileServer.ServeHTTP(w, r)
			return
		}

		// File doesn't exist. If the path has no extension, assume it's a
		// client-side SPA route and serve the app shell (history fallback) —
		// via serveIndex, not by rewriting r.URL.Path and delegating to
		// FileServer, which would re-trigger the index.html redirect above.
		if !hasFileExtension(r.URL.Path) {
			if serveIndex(w, r, distDir) {
				return
			}
		}

		// File doesn't exist and doesn't look like an SPA route.
		http.NotFound(w, r)
	})
}

// serveIndex writes the embedded index.html directly via http.ServeContent,
// at whatever path the caller requested (so SPA history-fallback routes don't
// get redirected away from themselves). Returns false if index.html isn't
// present in the embedded dist (e.g. the placeholder was removed without a
// real frontend build taking its place) so the caller can decide how to
// respond instead.
func serveIndex(w http.ResponseWriter, r *http.Request, distDir fs.FS) bool {
	f, err := distDir.Open("index.html")
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()

	stat, err := f.Stat()
	if err != nil {
		return false
	}

	modTime := stat.ModTime()
	if rs, ok := f.(io.ReadSeeker); ok {
		http.ServeContent(w, r, "index.html", modTime, rs)
		return true
	}
	// embed.FS files implement io.ReadSeeker in practice, but fall back to a
	// buffered read for any fs.FS that doesn't, rather than assuming.
	data, err := io.ReadAll(f)
	if err != nil {
		return false
	}
	http.ServeContent(w, r, "index.html", modTime, bytes.NewReader(data))
	return true
}

// hasFileExtension reports whether the path has a file extension.
func hasFileExtension(path string) bool {
	lastSlash := strings.LastIndex(path, "/")
	lastPart := path[lastSlash+1:]
	return strings.Contains(lastPart, ".")
}

// LoginPage returns the self-contained login HTML (no external assets).
func LoginPage() string {
	return `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>Agent Orchestrator - Login</title>
  <style>
    * {
      margin: 0;
      padding: 0;
      box-sizing: border-box;
    }

    body {
      font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", "Roboto", "Oxygen", "Ubuntu", "Cantarell", sans-serif;
      background: linear-gradient(135deg, #667eea 0%, #764ba2 100%);
      min-height: 100vh;
      display: flex;
      align-items: center;
      justify-content: center;
    }

    .container {
      background: white;
      border-radius: 8px;
      box-shadow: 0 10px 40px rgba(0, 0, 0, 0.2);
      width: 100%;
      max-width: 400px;
      padding: 40px;
    }

    .logo {
      text-align: center;
      margin-bottom: 30px;
    }

    .logo h1 {
      font-size: 24px;
      font-weight: 600;
      color: #333;
      margin-bottom: 8px;
    }

    .logo p {
      font-size: 14px;
      color: #666;
    }

    .form-group {
      margin-bottom: 20px;
    }

    label {
      display: block;
      font-size: 14px;
      font-weight: 500;
      color: #333;
      margin-bottom: 8px;
    }

    input[type="password"] {
      width: 100%;
      padding: 10px 12px;
      border: 1px solid #ccc;
      border-radius: 4px;
      font-size: 14px;
      font-family: inherit;
      transition: border-color 0.2s;
    }

    input[type="password"]:focus {
      outline: none;
      border-color: #667eea;
      box-shadow: 0 0 0 3px rgba(102, 126, 234, 0.1);
    }

    button {
      width: 100%;
      padding: 10px;
      background: #667eea;
      color: white;
      border: none;
      border-radius: 4px;
      font-size: 14px;
      font-weight: 600;
      cursor: pointer;
      transition: background 0.2s;
    }

    button:hover {
      background: #5568d3;
    }

    button:active {
      background: #4556b0;
    }

    .error {
      display: none;
      padding: 10px 12px;
      background: #fee;
      color: #c33;
      border: 1px solid #fcc;
      border-radius: 4px;
      font-size: 14px;
      margin-bottom: 20px;
    }

    .error.show {
      display: block;
    }

    .loading {
      display: none;
    }

    button.loading {
      opacity: 0.7;
      cursor: not-allowed;
    }

    button.loading .text {
      display: none;
    }

    button.loading .loading {
      display: inline;
    }
  </style>
</head>
<body>
  <div class="container">
    <div class="logo">
      <h1>Agent Orchestrator</h1>
      <p>Web Interface</p>
    </div>

    <div class="error" id="error"></div>

    <form id="loginForm">
      <div class="form-group">
        <label for="password">Password</label>
        <input type="password" id="password" name="password" required autofocus>
      </div>
      <button type="submit">
        <span class="text">Login</span>
        <span class="loading">Logging in...</span>
      </button>
    </form>
  </div>

  <script>
    const form = document.getElementById('loginForm');
    const passwordInput = document.getElementById('password');
    const errorDiv = document.getElementById('error');
    const submitButton = form.querySelector('button');

    form.addEventListener('submit', async (e) => {
      e.preventDefault();

      errorDiv.classList.remove('show');
      submitButton.classList.add('loading');
      submitButton.disabled = true;

      try {
        const response = await fetch('/api/v1/web/session', {
          method: 'POST',
          headers: {
            'Content-Type': 'application/json',
          },
          body: JSON.stringify({
            password: passwordInput.value,
          }),
        });

        if (!response.ok) {
          const data = await response.json();
          throw new Error(data.message || 'Login failed');
        }

        // Success: redirect to home
        window.location.href = '/';
      } catch (err) {
        errorDiv.textContent = err.message;
        errorDiv.classList.add('show');
      } finally {
        submitButton.classList.remove('loading');
        submitButton.disabled = false;
      }
    });
  </script>
</body>
</html>`
}
