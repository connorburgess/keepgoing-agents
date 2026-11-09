#!/bin/sh
# Example resume command. The prompt arrives on stdin; KEEPGOING_MODE is
# "auto" (start or steer a turn) or "queue" (queue behind the live turn).
prompt=$(cat)
printf 'would send in %s mode: %s\n' "$KEEPGOING_MODE" "$prompt" >&2
