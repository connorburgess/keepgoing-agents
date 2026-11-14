#!/bin/sh
# Script for a recurring BB automation. BB runs it on its own schedule;
# keepgoing does the deciding. Exit 75 (lock held or yielded) is not a failure.
# Replace auto_example1 with this automation's ID from BB's Automations: if the
# automation disables itself, keepgoing then names it when it says checks stopped.
keepgoing tick -c "$HOME/.config/agent-loops/example-bb-loop/keepgoing.yaml" --routine bb-automation/auto_example1
code=$?
[ "$code" -eq 75 ] && exit 0
exit "$code"
