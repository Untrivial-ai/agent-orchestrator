#!/bin/sh
set -eu

printf '\033[36mAO smoke agent ready\033[0m\r\n'
while IFS= read -r line; do
	printf 'received: %s\r\n' "$line"
done
