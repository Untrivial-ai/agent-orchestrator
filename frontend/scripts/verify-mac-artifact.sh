#!/usr/bin/env bash
#
# verify-mac-artifact.sh <path-to-.zip-or-.app-or-.dmg>
#
# The one canonical way to check that a shipped macOS artifact is sealed,
# notarized and stapled. CI gates and humans debugging by hand must both use
# this script, never a hand-typed variant.
#
# Why this exists (#3288 workstreams 1 and 2): on 28-29 Jul a human ran the
# check by hand, extracted the zip with plain `unzip`, and got a convincing but
# wrong "the signing pipeline corrupts the seal" answer. That paused all
# releases for two days. One script removes that failure mode by construction.
#
# The rules encoded here, all verified empirically on macOS 27.0. Do not
# "simplify" them; several contradict widely repeated advice:
#
#   1. Extract with `ditto -x -k`, NEVER `unzip`. Our zips embed AppleDouble
#      entries per directory (ditto without --sequesterRsrc). Plain unzip
#      materializes them as stray "._*" files inside the bundle, which breaks
#      the seal and makes codesign report a corrupt artifact that is in fact
#      fine. backend/internal/cli/start.go already encodes this rule in Go for
#      the `ao start` bootstrapper; this makes it reusable standalone.
#
#   2. `spctl -a -t exec` needs `-vv`. Without it an ACCEPTED artifact prints
#      nothing at all and merely exits 0, so any grep for "Notarized Developer
#      ID" can never match and the gate silently proves nothing.
#
#   3. `spctl` alone does not prove stapling. It can satisfy the notarization
#      check through Apple's online lookup, so it passes on a
#      notarized-but-unstapled artifact whenever the runner has network.
#      `xcrun stapler validate` is the check that proves the ticket is attached
#      to the bytes we ship, and it gates cleanly on exit code.
#
#   4. The spctl ASSESSMENT TYPE differs by artifact. An .app (however it was
#      delivered) is assessed as executable code, `-t exec`. A .dmg is not
#      executable code; it is a container Gatekeeper assesses on open, so it
#      needs `-t open --context context:primary-signature`, which is what #3267
#      decision 3 step 4 specifies. Using `-t exec` on a dmg assesses the wrong
#      thing. `-vv` stays mandatory for both (rule 2).
#
#   5. A valid bundle seal does not prove that nested executables received the
#      entitlements they need. The x64 ACP Node can pass `codesign --verify`
#      while crashing as soon as V8 compiles JavaScript (#3879). Zip/app checks
#      therefore inspect its final entitlements and execute the shipped binary.
#      The arch decision here is made from the binary itself (`lipo -archs`),
#      the same content-based invariant the signing-side selector implements
#      (frontend/makers/macho-archs.ts) — never from the host architecture.
#      Scope: this script is the local diagnostic and the public
#      mac-update-e2e baseline check; the pre-publication gate for canonical
#      releases is the release conductor's own verifier
#      (frontend/docs/desktop-release.md).
#
# Exit codes: 0 all checks passed, 1 a check failed, 2 usage error.

set -euo pipefail

usage() {
	cat >&2 <<'EOF'
usage: verify-mac-artifact.sh <path-to-.zip-or-.app-or-.dmg>

Verifies a macOS release artifact is signed, notarized and stapled:
  codesign --verify --deep --strict
  spctl -a -vv -t exec                                  (.zip / .app)
  spctl -a -vv -t open --context context:primary-signature   (.dmg)
  xcrun stapler validate
  bundled ACP Node entitlements + JavaScript execution       (.zip / .app)

A .zip is extracted with `ditto -x -k` first (never `unzip`), and the single
.app bundle inside it is checked. A .dmg is checked as the container it is,
without mounting it.
EOF
}

# --- Argument validation (exit 2). Runs before any macOS-only tooling is
# touched, so the usage paths are testable on any platform.

if [[ $# -ne 1 ]]; then
	usage
	exit 2
fi

case "$1" in
-h | --help)
	usage
	exit 2
	;;
esac

ARTIFACT="$1"

if [[ ! -e "$ARTIFACT" ]]; then
	echo "verify-mac-artifact: no such file: $ARTIFACT" >&2
	exit 2
fi

case "$ARTIFACT" in
*.zip)
	if [[ ! -f "$ARTIFACT" ]]; then
		echo "verify-mac-artifact: expected a .zip file, got a directory: $ARTIFACT" >&2
		exit 2
	fi
	;;
*.dmg)
	if [[ ! -f "$ARTIFACT" ]]; then
		echo "verify-mac-artifact: expected a .dmg file, got a directory: $ARTIFACT" >&2
		exit 2
	fi
	;;
*.app)
	if [[ ! -d "$ARTIFACT" ]]; then
		echo "verify-mac-artifact: expected an .app bundle directory: $ARTIFACT" >&2
		exit 2
	fi
	;;
*)
	echo "verify-mac-artifact: unsupported artifact type: $ARTIFACT (expected .zip, .app or .dmg)" >&2
	usage
	exit 2
	;;
esac

# --- Everything below needs a real macOS host with the Xcode command line
# tools. Checked explicitly so a Linux runner fails with a clear message rather
# than a bare "codesign: command not found".

if [[ "$(uname -s)" != "Darwin" ]]; then
	echo "verify-mac-artifact: macOS only (codesign/spctl/stapler); host is $(uname -s)" >&2
	exit 2
fi

for tool in codesign spctl xcrun ditto grep lipo plutil; do
	if ! command -v "$tool" >/dev/null 2>&1; then
		echo "verify-mac-artifact: required tool not found: $tool" >&2
		exit 2
	fi
done

WORKDIR=""
cleanup() {
	if [[ -n "$WORKDIR" ]]; then rm -rf "$WORKDIR"; fi
}
trap cleanup EXIT

APP="$ARTIFACT"
if [[ "$ARTIFACT" == *.zip ]]; then
	WORKDIR="$(mktemp -d)"
	echo "==> extracting with ditto (never unzip): $ARTIFACT"
	# `ditto -x -k` is the only extraction that preserves the seal. See rule 1.
	ditto -x -k "$ARTIFACT" "$WORKDIR"
	# `-maxdepth 1` deliberately: a nested .app (helpers inside the bundle) must
	# not be mistaken for the top-level one. Collected with a read loop rather
	# than mapfile, which macOS's stock bash 3.2 does not have.
	app_count=0
	while IFS= read -r candidate; do
		APP="$candidate"
		app_count=$((app_count + 1))
	done < <(find "$WORKDIR" -maxdepth 1 -name '*.app' -print)
	if [[ $app_count -ne 1 ]]; then
		echo "verify-mac-artifact: expected exactly one .app in $ARTIFACT, found $app_count" >&2
		exit 1
	fi
fi

echo "==> verifying: $APP"

failed=0
run_check() {
	local label="$1"
	shift
	echo "--> $label: $*"
	if "$@"; then
		echo "    ok: $label"
	else
		echo "::error::verify-mac-artifact: $label failed for $APP" >&2
		failed=1
	fi
}

has_acp_node_entitlement() {
	local entitlement="$1"
	codesign -d --entitlements :- "$ACP_NODE" 2>/dev/null \
		| plutil -p - \
		| grep -Fq "\"$entitlement\" => true"
}

# Seal intact. --deep walks nested code (helpers, frameworks, the bundled
# daemon); --strict rejects the loosened defaults. A .dmg has no nested code for
# --deep to walk, so the same invocation is correct for both.
run_check "codesign" codesign --verify --deep --strict --verbose=2 "$APP"

# Prove the nested runtime works, not merely that its signature is structurally
# valid. A dmg is intentionally not mounted by this verifier (rule 4), so the
# matching zip/app verification is the place where nested code is checked.
if [[ "$ARTIFACT" != *.dmg ]]; then
	ACP_NODE="$APP/Contents/Resources/acp-runtime/node/bin/node"
	run_check "ACP Node is bundled" test -x "$ACP_NODE"
	if [[ -x "$ACP_NODE" ]]; then
		run_check "ACP Node allow-jit entitlement" \
			has_acp_node_entitlement "com.apple.security.cs.allow-jit"
		echo "--> ACP Node architecture: lipo -archs $ACP_NODE"
		if acp_node_archs="$(lipo -archs "$ACP_NODE")"; then
			echo "    ok: ACP Node architecture ($acp_node_archs)"
			case " $acp_node_archs " in
			*" x86_64 "*)
				run_check "ACP Node x64 executable-memory entitlement (archs:$acp_node_archs)" \
					has_acp_node_entitlement "com.apple.security.cs.allow-unsigned-executable-memory"
				;;
			esac
		else
			echo "::error::verify-mac-artifact: ACP Node architecture inspection failed for $APP" >&2
			failed=1
		fi
	fi
fi

# Gatekeeper would accept it. -vv is mandatory (rule 2); expect
# "accepted" plus "source=Notarized Developer ID". The assessment type depends
# on the artifact (rule 4): executable code for an .app, "open" against the
# primary signature for a .dmg container.
if [[ "$ARTIFACT" == *.dmg ]]; then
	run_check "spctl" spctl -a -vv -t open --context context:primary-signature "$APP"
else
	run_check "spctl" spctl -a -vv -t exec "$APP"
fi

# The notarization ticket is actually attached to these bytes (rule 3).
run_check "stapler" xcrun stapler validate "$APP"

# Never execute code from an artifact until every trust check above has passed.
# Entitlement inspection is non-executing; this final smoke test is not.
if [[ $failed -eq 0 && "$ARTIFACT" != *.dmg ]]; then
	run_check "ACP Node JavaScript execution" "$ACP_NODE" -e \
		'console.log("ACP runtime JavaScript OK")'
fi

if [[ $failed -ne 0 ]]; then
	echo "verify-mac-artifact: FAILED for $ARTIFACT" >&2
	exit 1
fi

echo "verify-mac-artifact: OK ($ARTIFACT)"
