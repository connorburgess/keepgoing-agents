#!/bin/sh
# Example yield check: exit 0 when other work should run instead of this
# loop (keepgoing then skips the tick and exits 75), non-zero otherwise.
pgrep -f "example-nightly-build" >/dev/null
