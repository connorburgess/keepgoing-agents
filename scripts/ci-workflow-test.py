"""Regressions for native docs scope and the cheap pre-suite guard selector."""
from pathlib import Path
import re
import unittest

ROOT = Path(__file__).resolve().parents[1]
WORKFLOW = (ROOT / '.github/workflows/checks.yml').read_text()
GUARDS = {'TestDockHiddenTabReadsNothingG1', 'TestDockReadsTimeOutG2',
          'TestDockQuotaOnlyReadsAfterExplicitReview'}
SCOPE = "steps.scope.outputs.scope != 'docs'"


def validate(text):
    for job in ('windows-acl', 'windows-processes'):
        match = re.search(r'^  ' + job + r':\n(.*?)(?=^  [a-z][a-z-]*:|\Z)', text, re.M | re.S)
        assert match, f'missing {job}'
        steps = re.split(r'^      - ', match[1], flags=re.M)[1:]
        native = [step for step in steps if re.search(r'go (test|vet) ', step)]
        assert native, f'no native steps in {job}'
        for step in native:
            condition = re.search(r'^        if: (.+)$', step, re.M)
            assert condition and condition[1] in (SCOPE, f'always() && ({SCOPE})'), step
    steps = re.split(r'^      - ', text, flags=re.M)[1:]
    early = [s for s in steps if s.startswith('name: Check static panel read guards before the full suite\n')]
    assert len(early) == 1, 'missing or duplicate early step'
    match = re.search(r"run: go test -race ./cmd/looptender -run '\^\(([^']+)\)\$' -count=1$", early[0], re.M)
    assert match, 'early command must propagate go test failure'
    names = match[1].split('|')
    assert len(names) == len(GUARDS) and set(names) == GUARDS, 'guard omitted or duplicated'
    source = (ROOT / 'cmd/looptender/dock_hidden_read_test.go').read_text()
    assert GUARDS <= set(re.findall(r'^func (Test\w+)\(t \*testing.T\)', source, re.M)), 'guard renamed or removed'
    assert f"if: {SCOPE} || matrix.os == 'ubuntu-24.04'" in early[0]
    early_at = text.index(early[0])
    for command in ('go vet ./...', 'go build ./...', 'go test -race ./...'):
        assert text.count('run: ' + command + '\n') == 1, f'missing or duplicate {command}'
        assert early_at < text.index('run: ' + command + '\n'), f'guards follow {command}'


class WorkflowTests(unittest.TestCase):
    def test_current_workflow(self):
        validate(WORKFLOW)

    def test_unguarded_assignment_rejected(self):
        bad = WORKFLOW.replace('name: Verify explicit assignment lifecycle and coordination records on Windows\n        if: ' + SCOPE,
                               'name: Verify explicit assignment lifecycle and coordination records on Windows')
        with self.assertRaises(AssertionError):
            validate(bad)

    def test_accidentally_skipped_native_coverage_rejected(self):
        with self.assertRaises(AssertionError):
            validate(WORKFLOW.replace("run: go vet ./cmd/looptender\n        if: " + SCOPE, "run: go vet ./cmd/looptender\n        if: steps.scope.outputs.scope == 'docs'", 1))

    def test_missing_duplicate_or_nonpropagating_guard_rejected(self):
        for replacement in ('TestMissing', 'TestDockHiddenTabReadsNothingG1', 'TestDockReadsTimeOutG2|TestDockReadsTimeOutG2'):
            with self.subTest(replacement=replacement), self.assertRaises(AssertionError):
                validate(WORKFLOW.replace('TestDockReadsTimeOutG2', replacement))
        with self.assertRaises(AssertionError):
            validate(WORKFLOW.replace('run: go test -race ./cmd/looptender', 'run: echo go test -race ./cmd/looptender'))

        with self.assertRaises(AssertionError):
            validate(WORKFLOW.replace("$' -count=1\n", "$' -count=1 || true\n"))

    def test_full_race_suite_cannot_be_replaced_by_selection(self):
        with self.assertRaises(AssertionError):
            validate(WORKFLOW.replace('run: go test -race ./...\n', "run: go test -race ./... -run '^TestDock'\n"))


if __name__ == '__main__':
    unittest.main()
