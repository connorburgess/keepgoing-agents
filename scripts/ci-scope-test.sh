#!/usr/bin/env bash
# Table test for scripts/ci-scope.sh. Run from the repository root.
set -u
fail=0
check() { # check WANT PATH...
	local want=$1 got
	shift
	got=$(printf '%s\n' "$@" | scripts/ci-scope.sh --classify 2>/dev/null)
	if [ "$got" != "scope=$want" ]; then
		echo "FAIL: [$*] gave $got, want scope=$want"
		fail=1
	fi
}

# Documentation.
check docs README.md
check docs CHANGELOG.md AGENTS.md CONTRIBUTING.md SECURITY.md
check docs docs/roadmap.md docs/assets/example.png docs/releases/v0.1.5.md
check docs LICENSE
check docs LICENSES/Apache-2.0.txt LICENSING NOTICE LICENSE.txt
check docs integrations/macos/README.md "docs/café-项目.md"

# Anything else runs everything.
check full ""
check full cmd/keepgoing/main.go
check full README.md internal/decide/decide.go
check full internal/README.md
check full cmd/keepgoing/testdata/example.md
check full integrations/bb-plugin-keepgoing/src/testdata/example.md
check full integrations/bb-plugin-keepgoing/package.json
check full integrations/macos/Package.swift
check full scripts/ci-scope.sh
check full scripts/README.md
check full .github/workflows/checks.yml
check full .github/pull_request_template.md
check full schema/keepgoing.schema.json
check full examples/keepgoing.bb.yaml
check full examples/README.md
check full go.mod
check full go.sum
check full .gitignore
check full .bb/plugins.json
check full docs.go
check full LICENSE.go.bak/main.go
check full LICENSE.d/build.sh NOTICE.d/x
check full '"docs/odd\nname.md"'
check full README.md Makefile

# The event decides before any file is looked at.
for event in push schedule ""; do
	got=$(EVENT=$event scripts/ci-scope.sh 2>/dev/null)
	[ "$got" = "scope=full" ] || { echo "FAIL: event '$event' gave $got"; fail=1; }
done
got=$(EVENT=workflow_dispatch scripts/ci-scope.sh 2>/dev/null)
[ "$got" = "scope=full" ] || { echo "FAIL: a manual run without a base gave $got"; fail=1; }
got=$(EVENT=workflow_dispatch PREVIEW_BASE=no-such-commit scripts/ci-scope.sh 2>/dev/null)
[ "$got" = "scope=full" ] || { echo "FAIL: an unknown preview base gave $got"; fail=1; }

[ "$fail" = 0 ] && echo "ci-scope: all cases pass"
exit "$fail"
