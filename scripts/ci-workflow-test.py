"""Regressions for native docs scope and the full race runner entry point."""
from pathlib import Path
import re
import unittest

ROOT = Path(__file__).resolve().parents[1]
WORKFLOW = (ROOT / '.github/workflows/checks.yml').read_text()
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
    race = [s for s in steps if s.startswith('name: Run native race suite with disjoint command partitions\n')]
    assert len(race) == 1, 'missing or duplicate native race step'
    assert re.search(r'^        run: python3 -u scripts/ci-race.py$', race[0], re.M), 'native runner failure must propagate'
    assert f"if: {SCOPE} || matrix.os == 'ubuntu-24.04'" in race[0]
    assert not re.search(r'run: go test -race ./cmd/looptender -run', text), 'static guards already execute once in the native runner'
    for script in ('ci-scope-test.sh', 'check-contributions-test.sh', 'ci-timeout-note-test.sh', 'ci-workflow-test.py'):
        assert text.index(script) < text.index('run: go vet ./...'), f'{script} must fail before expensive checks'


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

    def test_missing_or_nonpropagating_native_runner_rejected(self):
        for replacement in ('echo skipped', 'python3 -u scripts/ci-race.py || true', 'go test -race ./cmd/looptender'):
            with self.subTest(replacement=replacement), self.assertRaises(AssertionError):
                validate(WORKFLOW.replace('python3 -u scripts/ci-race.py', replacement))

    def test_redundant_static_guard_execution_rejected(self):
        with self.assertRaises(AssertionError):
            validate(WORKFLOW + "\n      - run: go test -race ./cmd/looptender -run '^TestDock'\n")


if __name__ == '__main__':
    unittest.main()
