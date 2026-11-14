#!/bin/sh
# Example resume command. The prompt arrives on stdin; LOOPTENDER_MODE is
# "auto" (start or steer a turn) or "queue" (queue behind the live turn). It
# also arrives as KEEPGOING_MODE, its earlier name.
prompt=$(cat)
printf 'would send in %s mode: %s\n' "$LOOPTENDER_MODE" "$prompt" >&2
