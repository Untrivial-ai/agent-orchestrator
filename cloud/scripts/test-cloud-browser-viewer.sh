#!/usr/bin/env bash
set -euo pipefail

script_directory="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
case "${1:-}" in
	""|--local) ;;
	*)
		echo "Usage: $0 [--local]" >&2
		exit 2
		;;
esac
export AO_CLOUD_BROWSER_VIEWER=1
export AO_CLOUD_BROWSER_VIEWER_E2E=1
exec "$script_directory/test-cloud-local.sh"
