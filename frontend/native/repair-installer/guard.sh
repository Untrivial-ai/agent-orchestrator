#!/bin/bash
# Shared by Installer's GUI check and root preinstall; all values are build-time constants.
set -eu
export PATH=/usr/bin:/bin:/usr/sbin:/sbin
fail() { echo "Agent Orchestrator repair: $*" >&2; exit 1; }
@@ROOT_CHECK@@
[ "${3:-}" = / ] || fail 'Install only on the startup volume.'
app='/Applications/Agent Orchestrator.app'
[ ! -L /Applications ] && [ ! -L "$app" ] || fail 'The destination must not be a symbolic link.'
[ ! -e "$app" ] || [ -d "$app" ] || fail 'The destination is not an application bundle.'
# sysctl identifies Apple Silicon even when Installer runs under Rosetta.
if [ "$(/usr/sbin/sysctl -n hw.optional.arm64 2>/dev/null || true)" = 1 ]; then
  host=arm64
else
  host=$(/usr/bin/uname -m) || fail 'Cannot identify this Mac architecture.'
fi
case ' @@ARCHS@@ ' in *" $host "*) ;; *) fail 'This installer does not match this Mac architecture.' ;; esac
processes=$(/bin/ps -axo comm=) || fail 'Cannot check running applications.'
# comm is the executable path, not arbitrary arguments mentioning AO.
if printf '%s\n' "$processes" | /usr/bin/awk '
  /\/Contents\/MacOS\/agent-orchestrator$/ { found=1 }
  /\/Agent Orchestrator[.]app\/Contents\// { found=1 }
  /\/dev[.]agent-orchestrator[.]desktop[.]ShipIt\/.*\/ShipIt$/ { found=1 }
  /\/dev[.]agent-orchestrator[.]desktop[.]ShipIt\/ShipIt$/ { found=1 }
  END { exit !found }'; then
  fail 'Quit Agent Orchestrator. If its updater is still running, restart your Mac and run this installer before opening AO.'
fi
# A renamed AO bundle can still host ShipIt after its main process exits.
# Resolve the enclosing bundle identity without running any code from it.
while IFS= read -r process; do
  case "$process" in
    *.app/Contents/*/ShipIt)
      owner="${process%%.app/Contents/*}.app/Contents/Info.plist"
      owner_id=$(/usr/libexec/PlistBuddy -c 'Print :CFBundleIdentifier' "$owner" 2>/dev/null || true)
      [ "$owner_id" != dev.agent-orchestrator.desktop ] || fail 'AO updater is running. Restart your Mac and run this installer before opening AO.'
      ;;
  esac
done <<< "$processes"
if [ -e "$app" ]; then
  [ ! -L "$app/Contents" ] && [ ! -L "$app/Contents/Info.plist" ] || fail 'The existing bundle metadata must not be a symbolic link.'
  plist="$app/Contents/Info.plist"
  id=$(/usr/libexec/PlistBuddy -c 'Print :CFBundleIdentifier' "$plist") || fail 'Cannot read the existing bundle identity.'
  [ "$id" = dev.agent-orchestrator.desktop ] || fail 'Refusing to replace an unrelated application.'
  old=$(/usr/libexec/PlistBuddy -c 'Print :CFBundleShortVersionString' "$plist") || fail 'Cannot read the existing version.'
  build=$(/usr/libexec/PlistBuddy -c 'Print :CFBundleVersion' "$plist") || fail 'Cannot read the existing build.'
  [ "$old" = "$build" ] || fail 'Unrecognized version/build combination; refusing replacement.'
  # Supported release grammar only. Unknown channels/builds fail closed.
  /usr/bin/awk -v old="$old" -v new='@@VERSION@@' '
    function valid(v, fields, count) {
      if (v !~ /^(0|[1-9][0-9]*)[.](0|[1-9][0-9]*)[.](0|[1-9][0-9]*)(-nightly[.][0-9]+)?$/) return 0;
      count=split(v,fields,/[-.]/);
      return count==3 || (count==5 && length(fields[5])==12);
    }
    BEGIN {
      if (!valid(old) || !valid(new)) exit 1;
      split(old,a,/[-.]/); split(new,b,/[-.]/);
      for (i=1;i<=3;i++) { if (b[i]+0 > a[i]+0) exit 0; if (b[i]+0 < a[i]+0) exit 1; }
      if (old==new) exit 0;
      if (a[4]=="" && b[4]!="") exit 1;
      if (b[4]=="" && a[4]!="") exit 0;
      exit !(b[5]+0 >= a[5]+0);
    }' || fail 'Downgrades and unrecognized versions are not supported by this repair installer.'
fi
exit 0
