#!/usr/bin/env bash
# Regenerate every mobile icon asset from the desktop app icon.
#
#   packages/mobile/scripts/generate-icons.sh
#
# Source of truth: frontend/assets/icon.png -- the Electron icon, the same artwork
# packed into icon.icns (macOS) and icon.ico (Windows). icon-build/from-desktop.py
# strips the macOS tile's margin, rim and rounded corners, lifts Ruto off the tile
# face, and re-lays both for each platform so the phone icon matches the desktop.
# See the docstring there for what each output is.
#
# Outputs land in packages/mobile/assets/: the Icon Composer bundle AO.icon (iOS
# 26 Liquid Glass) plus the flat rasters used by Android, web and Expo Go.
#
# Requirements (this is a manual, out-of-band tool -- the generated assets are
# committed, so nothing in the app build or CI runs it):
#   * Python 3 with Pillow      pip3 install Pillow
#
# Verify the Liquid Glass result without a build:
#   scripts/preview-icon.sh
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
mobile="$(dirname "$here")"
assets="$mobile/assets"
root="$(cd "$mobile/../.." && pwd)"
source_icon="$root/frontend/assets/icon.png"

# Fail with an actionable message rather than a traceback.
command -v python3 >/dev/null \
  || { echo "python3 not found -- required by from-desktop.py" >&2; exit 1; }
python3 -c 'import PIL' 2>/dev/null \
  || { echo "Pillow not installed -- run: pip3 install Pillow" >&2; exit 1; }
[ -f "$source_icon" ] || { echo "desktop icon not found: $source_icon" >&2; exit 1; }
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

echo "==> deriving icons from frontend/assets/icon.png"
python3 "$here/icon-build/from-desktop.py" "$source_icon" "$work"

echo "==> installing into assets/"
rm -rf "$assets/AO.icon"
cp -R "$work/AO.icon" "$assets/AO.icon"
for f in icon.png splash-icon.png favicon.png android-icon-background.png \
         android-icon-foreground.png android-icon-monochrome.png; do
  cp "$work/$f" "$assets/$f"
done

echo "==> done"
ls -1 "$assets"
