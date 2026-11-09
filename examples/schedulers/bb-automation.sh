#!/bin/sh
# Script for a recurring BB automation. BB runs it on its own schedule;
# keepgoing does the deciding. Exit 75 (lock held or yielded) is not a failure.
keepgoing tick -c "$HOME/.config/keepgoing/example-bb-loop/keepgoing.yaml"
code=$?
[ "$code" -eq 75 ] && exit 0
exit "$code"
