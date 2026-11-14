#!/usr/bin/env bash
# Rebuild the state fixtures in this directory: one state directory per
# released version, written by that release's own binary.
#
#   generate.sh <work-directory>
#
# The work directory must be outside any checkout and is where each release
# is checked out and built (about 100 MB). Run from the repository root with
# every release tag fetched. Each release is driven through the same journey
# against the scenario suite's stand-in bb: enable, a wake, a check on a
# working session, a re-prompt after an unclassified error, a check inside the
# backoff, then `await-user` with a note and one held check. So every fixture
# is a loop held for a person, with a send history and one re-prompt spent.
#
# Check times are injected (`tick --now`). control.json's own timestamp is
# the wall clock at generation, so it differs between runs.
set -euo pipefail
work=${1:?usage: generate.sh <work-directory>}
root=$(git rev-parse --show-toplevel)
out="$root/internal/scenario/testdata/released"
mkdir -p "$work"
work=$(cd "$work" && pwd)
case "$work/" in "$root"/*) echo "the work directory must be outside the checkout" >&2; exit 2 ;; esac

go build -o "$work/bb" "$root/internal/scenario/standin"

for tag in v0.1.0 v0.1.1 v0.1.2 v0.1.3 v0.1.4 v0.1.5; do
	src="$work/src-$tag"
	[ -d "$src" ] || git -C "$root" worktree add -q --detach "$src" "$tag"
	(cd "$src" && go build -o "$work/keepgoing-$tag" ./cmd/keepgoing)
	loop="$work/loop-$tag"
	rm -rf "$loop" && mkdir -p "$loop/world"
	cat > "$loop/keepgoing.yaml" <<YAML
version: 1
loop:
  name: example-loop
  state_dir: state
  adapter: bb
  provider: claude-code
  bb: {thread: thr_example_primary, cli: $work/bb}
  prompt: Finish the fictional example task and report what changed.
YAML
	kg() { (cd "$loop" && env -i PATH="$work" HOME="$loop/home" SCENARIO_WORLD="$loop/world" BB_THREAD_ID= "$work/keepgoing-$tag" "$@" -c keepgoing.yaml); }
	show() { printf '{"thread":{"id":"thr_example_primary","status":"%s"}}' "$1" > "$loop/world/thr_example_primary.show"; }
	show idle
	echo '[]' > "$loop/world/thr_example_primary.log"
	kg enable >/dev/null
	kg tick --now 2026-03-02T09:01:00Z >/dev/null
	show active
	echo '[{"seq":1,"type":"turn/started","createdAt":1772442060000,"data":{}}]' > "$loop/world/thr_example_primary.log"
	kg tick --now 2026-03-02T09:06:00Z >/dev/null
	show error
	echo '[{"seq":1,"type":"turn/started","createdAt":1772442060000,"data":{}},{"seq":2,"type":"provider/error","createdAt":1772442600000,"data":{"willRetry":false,"errorInfo":{"category":"unknown","httpStatusCode":null,"providerCode":"unknown"}}}]' > "$loop/world/thr_example_primary.log"
	kg tick --now 2026-03-02T09:11:00Z >/dev/null
	kg tick --now 2026-03-02T09:12:00Z >/dev/null
	kg await-user --note "Fictional: choose the release name" >/dev/null
	kg tick --now 2026-03-02T09:20:00Z >/dev/null
	# Locks are empty and recreated on demand; leave them out.
	find "$loop/state" -name '*.lock' -delete
	rm -rf "${out:?}/$tag" && mkdir -p "$out/$tag"
	cp -R "$loop/state" "$out/$tag/state"
	echo "$tag: $(cd "$out/$tag" && find state -type f | sort | tr '\n' ' ') sends=$(grep -c tell "$loop/world/calls.jsonl")"
done
