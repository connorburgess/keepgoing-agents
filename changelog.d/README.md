# Changelog fragments

Each change that needs a changelog entry adds one file here instead of editing
`CHANGELOG.md`. Two pull requests then never edit the same lines, so merging
one does not put the others in conflict, and the merge queue can take them in
turn.

- Name: lower case, ending in `.md`, directly in this directory, for example
  `changelog.d/panel-usage-page.md`. Pick a name no other change is likely to
  use; the branch's topic is a good one.
- Content: the entry, written as an entry under "Unreleased" in
  `CHANGELOG.md` is written: usually one paragraph starting with `- `. No
  heading.
- The contribution check refuses a line added under `## Unreleased` in
  `CHANGELOG.md`, and a fragment that is empty, has a heading, or is named or
  placed otherwise.

At release time, `scripts/changelog-release.sh <version> <date>` moves every
fragment, newest first, and the entries already under Unreleased into a new
`## <version> (<date>)` section and deletes the fragments. Run it in the
release-prep pull request.

## Moving an open pull request

A pull request opened before fragments, with a line under Unreleased, is
refused on its next push. To move it:

1. Merge main into it.
2. Move your Unreleased entry into `changelog.d/<topic>.md`.
3. Restore `CHANGELOG.md` to main's version:
   `git checkout origin/main -- CHANGELOG.md`.
