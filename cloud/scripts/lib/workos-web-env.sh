#!/usr/bin/env bash

configure_workos_web() {
	local configured_web_port="$1"
	local callback_host="${2:-localhost}"
	local aws_profile="${AWS_PROFILE:-ao-cloud}"
	local secret_id="${AO_CLOUD_STAGING_WORKOS_SECRET_ID:-ao-cloud/staging/workos}"
	local state_dir="${AO_CLOUD_WEB_STATE_DIR:-$HOME/.ao/cloud-web}"
	local cookie_file="$state_dir/auth-cookie-password"
	local secret_json
	if [[ "$callback_host" != "localhost" && "$callback_host" != "127.0.0.1" ]]; then
		echo "WorkOS callback host must be localhost or 127.0.0.1." >&2
		return 1
	fi

	if [[ -z "${WORKOS_API_KEY:-}" || -z "${WORKOS_CLIENT_ID:-}" ]]; then
		secret_json="$(
			aws secretsmanager get-secret-value \
				--profile "$aws_profile" \
				--secret-id "$secret_id" \
				--query SecretString \
				--output text
		)"
		export WORKOS_API_KEY
		WORKOS_API_KEY="$(
			SECRET_JSON="$secret_json" python3 - <<'PY'
import json
import os

payload = json.loads(os.environ["SECRET_JSON"])
names = ("api_key", "apiKey", "WORKOS_API_KEY")
value = next((payload.get(name) for name in names if payload.get(name)), "")
if not value:
    raise SystemExit(f"WorkOS secret is missing one of: {', '.join(names)}")
print(value)
PY
		)"
		export WORKOS_CLIENT_ID
		WORKOS_CLIENT_ID="$(
			SECRET_JSON="$secret_json" python3 - <<'PY'
import json
import os

payload = json.loads(os.environ["SECRET_JSON"])
names = ("client_id", "clientId", "WORKOS_CLIENT_ID")
value = next((payload.get(name) for name in names if payload.get(name)), "")
if not value:
    raise SystemExit(f"WorkOS secret is missing one of: {', '.join(names)}")
print(value)
PY
		)"
	fi
	unset secret_json

	mkdir -p "$state_dir"
	chmod 700 "$state_dir"
	if [[ ! -s "$cookie_file" ]]; then
		umask 077
		openssl rand -base64 32 >"$cookie_file"
	fi
	export WORKOS_COOKIE_PASSWORD
	WORKOS_COOKIE_PASSWORD="$(<"$cookie_file")"
	export WORKOS_REDIRECT_URI="http://${callback_host}:${configured_web_port}/callback"
	export NEXT_PUBLIC_WORKOS_REDIRECT_URI="$WORKOS_REDIRECT_URI"

	WORKOS_REDIRECT_STATUS="$(
		python3 - <<'PY'
import json
import os
import urllib.error
import urllib.request

target = os.environ["WORKOS_REDIRECT_URI"]
headers = {
    "Authorization": f"Bearer {os.environ['WORKOS_API_KEY']}",
    "Content-Type": "application/json",
}
request = urllib.request.Request(
    "https://api.workos.com/user_management/redirect_uris?limit=100",
    headers=headers,
)
with urllib.request.urlopen(request, timeout=20) as response:
    payload = json.load(response)
items = payload.get("data") or payload.get("redirect_uris") or []
if any(item.get("uri") == target for item in items if isinstance(item, dict)):
    print("present")
else:
    body = json.dumps({"uri": target}).encode()
    request = urllib.request.Request(
        "https://api.workos.com/user_management/redirect_uris",
        data=body,
        headers=headers,
        method="POST",
    )
    try:
        with urllib.request.urlopen(request, timeout=20):
            pass
    except urllib.error.HTTPError as error:
        if error.code != 409:
            raise
    print("created")
PY
	)"
	export WORKOS_REDIRECT_STATUS
	export AO_CLOUD_WORKOS_SECRET_ID="$secret_id"
	export AO_CLOUD_WORKOS_AWS_PROFILE="$aws_profile"
}
