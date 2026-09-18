#!/usr/bin/env python3
"""Connect a contributor's local coding-agent credential to their AO Cloud org.

Creating a cloud session requires a validated coding-agent provider connection
(the sandbox worker needs the agent's key). The desktop app has no UI for this
yet, so this dev tool pushes your local OpenCode API key to the control plane
so you can create cloud sessions and test the flow.

It (1) reads your OpenCode API key from the usual local locations,
(2) signs in to the control plane with WorkOS (browser, one time; the token is
cached and refreshed after that), and (3) PUTs the opencode provider
connection for your org, printing the validation result.

Usage:
    python3 cloud/scripts/dev-connect-agent-credential.py            # connect
    python3 cloud/scripts/dev-connect-agent-credential.py --login    # force re-login
    python3 cloud/scripts/dev-connect-agent-credential.py --delete   # remove the connection

Environment overrides:
    AO_CLOUD_CONTROL_PLANE_URL   control-plane base (default: staging)
    AO_CLOUD_WORKOS_CLIENT_ID    WorkOS client id (default: staging)
    AO_CLOUD_AUTH_REDIRECT       loopback redirect (default: http://127.0.0.1:3000/callback)
    OPENCODE_API_KEY             raw API key, overrides local lookup
    OPENCODE_DATA_DIR            opencode data dir (default: ~/.local/share/opencode)
    AO_CLOUD_TOKEN_FILE          where the WorkOS token is cached (default: ~/.ao/cloud-dev-token.json)

The loopback redirect URI must be registered on the WorkOS AuthKit client.
"""

import base64
import hashlib
import http.server
import json
import os
import secrets
import subprocess
import sys
import threading
import time
import urllib.error
import urllib.parse
import urllib.request
import webbrowser

CP = os.environ.get("AO_CLOUD_CONTROL_PLANE_URL", "https://staging-api.aoagents.dev").rstrip("/")
API = CP + "/api/cloud/v1"
CLIENT_ID = os.environ.get("AO_CLOUD_WORKOS_CLIENT_ID", "client_01KZ3VRKC374HS91XGRDPT3671")
REDIRECT = os.environ.get("AO_CLOUD_AUTH_REDIRECT", "http://127.0.0.1:3000/callback")
TOKEN_FILE = os.path.expanduser(os.environ.get("AO_CLOUD_TOKEN_FILE", "~/.ao/cloud-dev-token.json"))
WORKOS = "https://api.workos.com/user_management"


# --- OpenCode credential lookup -----------------------------------------------

def _extract_api_key(raw: str) -> str:
    """Accept either a bare key or wrapped JSON like {provider:{type,key}}."""
    raw = raw.strip()
    if not raw.startswith("{"):
        return raw

    def first_key(obj) -> str:
        if isinstance(obj, dict):
            for field in ("key", "api_key", "accessToken", "access_token", "token"):
                value = obj.get(field)
                if isinstance(value, str) and value.strip():
                    return value.strip()
        return ""

    data = json.loads(raw)
    direct = first_key(data)
    if direct:
        return direct
    for entry in data.values():
        if isinstance(entry, dict):
            direct = first_key(entry)
            if direct:
                return direct
    return ""


def opencode_data_dir() -> str:
    data_dir = os.environ.get("OPENCODE_DATA_DIR", "").strip()
    if data_dir:
        return os.path.expanduser(data_dir)
    xdg = os.environ.get("XDG_DATA_HOME", "").strip()
    if xdg:
        return os.path.join(xdg, "opencode")
    return os.path.expanduser("~/.local/share/opencode")


def read_opencode_credential() -> str:
    env = os.environ.get("OPENCODE_API_KEY", "").strip()
    if env:
        key = _extract_api_key(env)
        if key:
            return key
    path = os.path.join(opencode_data_dir(), "auth.json")
    if os.path.exists(path):
        with open(path) as f:
            key = _extract_api_key(f.read())
        if key:
            return key
    raise SystemExit(
        "No OpenCode API key found. Set OPENCODE_API_KEY or sign in to opencode "
        f"first (auth.json at {path})."
    )


# --- WorkOS token (cache + refresh + one-time PKCE login) ---------------------

def _b64url(b: bytes) -> str:
    return base64.urlsafe_b64encode(b).rstrip(b"=").decode()


def _exp(token: str) -> int:
    try:
        p = token.split(".")[1]
        p += "=" * (-len(p) % 4)
        return json.loads(base64.urlsafe_b64decode(p)).get("exp", 0)
    except Exception:
        return 0


def _save(tok: dict) -> None:
    os.makedirs(os.path.dirname(TOKEN_FILE), exist_ok=True)
    with open(TOKEN_FILE, "w") as f:
        json.dump(tok, f)
    os.chmod(TOKEN_FILE, 0o600)


def _post_workos(body: dict) -> dict:
    data = json.dumps(body).encode()
    req = urllib.request.Request(
        WORKOS + "/authenticate", data=data, headers={"Content-Type": "application/json"}
    )
    with urllib.request.urlopen(req, timeout=30) as r:
        return json.loads(r.read())


def pkce_login() -> dict:
    verifier = _b64url(secrets.token_bytes(48))
    challenge = _b64url(hashlib.sha256(verifier.encode()).digest())
    state = _b64url(secrets.token_bytes(16))
    redirect = urllib.parse.urlparse(REDIRECT)
    port = redirect.port or 3000

    captured: dict = {}
    done = threading.Event()

    class Handler(http.server.BaseHTTPRequestHandler):
        def log_message(self, *a):  # silence
            pass

        def do_GET(self):
            q = urllib.parse.parse_qs(urllib.parse.urlparse(self.path).query)
            captured["code"] = (q.get("code") or [""])[0]
            captured["state"] = (q.get("state") or [""])[0]
            self.send_response(200)
            self.send_header("Content-Type", "text/html")
            self.end_headers()
            self.wfile.write(b"<h3>AO Cloud dev login complete. Close this tab.</h3>")
            done.set()

    server = http.server.HTTPServer((redirect.hostname or "127.0.0.1", port), Handler)
    threading.Thread(target=server.handle_request, daemon=True).start()

    authorize = WORKOS + "/authorize?" + urllib.parse.urlencode({
        "client_id": CLIENT_ID, "redirect_uri": REDIRECT, "response_type": "code",
        "provider": "authkit", "code_challenge": challenge,
        "code_challenge_method": "S256", "state": state,
    })
    print("Opening browser to sign in to AO Cloud...")
    webbrowser.open(authorize)
    print("If the browser did not open, visit:\n  " + authorize)

    if not done.wait(timeout=300):
        raise SystemExit("Timed out waiting for the WorkOS callback.")
    if not captured.get("code") or captured.get("state") != state:
        raise SystemExit("WorkOS callback was invalid (state mismatch or no code).")

    resp = _post_workos({
        "client_id": CLIENT_ID, "grant_type": "authorization_code",
        "code": captured["code"], "code_verifier": verifier,
    })
    tok = {"access_token": resp["access_token"],
           "refresh_token": resp.get("refresh_token", ""),
           "client_id": CLIENT_ID}
    _save(tok)
    return tok


def bearer(force_login: bool) -> str:
    if force_login or not os.path.exists(TOKEN_FILE):
        return pkce_login()["access_token"]
    tok = json.load(open(TOKEN_FILE))
    if _exp(tok.get("access_token", "")) - int(time.time()) >= 60:
        return tok["access_token"]
    if not tok.get("refresh_token"):
        return pkce_login()["access_token"]
    try:
        resp = _post_workos({
            "client_id": tok.get("client_id", CLIENT_ID),
            "grant_type": "refresh_token", "refresh_token": tok["refresh_token"],
        })
    except urllib.error.HTTPError:
        return pkce_login()["access_token"]
    tok["access_token"] = resp["access_token"]
    if resp.get("refresh_token"):
        tok["refresh_token"] = resp["refresh_token"]
    _save(tok)
    return tok["access_token"]


# --- Control-plane calls -----------------------------------------------------

def cp(method: str, path: str, token: str, body=None, timeout=90):
    headers = {"Authorization": "Bearer " + token, "Content-Type": "application/json"}
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(API + path, data=data, method=method, headers=headers)
    try:
        with urllib.request.urlopen(req, timeout=timeout) as r:
            return r.status, json.loads(r.read() or "{}")
    except urllib.error.HTTPError as e:
        try:
            return e.code, json.loads(e.read() or b"{}")
        except Exception:
            return e.code, {}


def resolve_org(token: str) -> str:
    st, me = cp("GET", "/me", token)
    if st != 200:
        raise SystemExit(f"GET /me failed ({st}). Re-run with --login.")
    orgs = me.get("organizations") or []
    if orgs:
        return orgs[0]["id"]
    user = me.get("user") or {}
    name = (user.get("firstName") or user.get("email") or "Workspace").split("@")[0]
    st, r = cp("POST", "/orgs", token, {"displayName": name[:80]})
    return (r.get("organization") or {}).get("id")


def main() -> None:
    force_login = "--login" in sys.argv
    delete = "--delete" in sys.argv
    token = bearer(force_login)
    org = resolve_org(token)
    print(f"control plane: {CP}\norg: {org}")

    if delete:
        st, _ = cp("DELETE", f"/orgs/{org}/provider-connections/agents/opencode", token)
        print(f"delete connection: {st}")
        return

    secret = read_opencode_credential()
    st, r = cp("PUT", f"/orgs/{org}/provider-connections/agents/opencode", token,
               {"credentialType": "api_key", "secret": secret})
    validation = (r.get("providerConnection") or {}).get("validationState")
    print(f"connect opencode: HTTP {st} | validation: {validation}")
    if st >= 400 or validation not in ("valid", None):
        print("  response:", json.dumps(r)[:300])
        sys.exit(1)
    print("Done. You can now create cloud sessions in this org.")


if __name__ == "__main__":
    main()