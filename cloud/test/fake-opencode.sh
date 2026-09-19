#!/bin/sh
set -eu

# Minimal opencode stand-in for smoke tests: print a ready marker and echo
# whatever the orchestrator types into the PTY.
printf '\033[36mAO smoke opencode ready\033[0m\r\n'
while IFS= read -r line; do
	printf 'received: %s\r\n' "$line"
done