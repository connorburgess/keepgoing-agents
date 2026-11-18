"""Provider-free regressions for selection, execution evidence and failure paths."""
import importlib.util
import json
from pathlib import Path
import re
import subprocess
import sys
import tempfile
import time
import unittest

spec = importlib.util.spec_from_file_location('ci_race', Path(__file__).with_name('ci-race.py'))
race = importlib.util.module_from_spec(spec)
spec.loader.exec_module(race)


class PartitionTests(unittest.TestCase):
    def test_new_tests_examples_and_fuzz_seeds_exactly_once(self):
        names = sorted(race.EARLY) + ['TestNew', 'TestNewest', 'Example', 'ExampleOutput', 'FuzzSeeds']
        listed = race.inventory('\n'.join(names + ['BenchmarkCost']))
        groups = race.partition(listed)
        self.assertCountEqual([n for g in groups for n in g], names)
        self.assertEqual(groups[0], sorted(race.EARLY))
        self.assertFalse(set(groups[0]) & set(groups[1] + groups[2]))
        for name in ['Example', 'ExampleOutput', 'FuzzSeeds', 'TestNewest']:
            self.assertEqual(sum(name in g for g in groups), 1)

    def test_missing_duplicate_empty_and_unrecognized_inventory_refused(self):
        for output in ['', 'TestA\nTestA', 'TestA\nFAIL', 'TestA\nwarning: partial list']:
            with self.subTest(output=output), self.assertRaises(ValueError):
                race.inventory(output)
        with self.assertRaises(ValueError):
            race.partition(['TestNew', 'TestOther'])
        for groups in [[['TestA']], [['TestA'], ['TestA', 'TestB']], [['TestA', 'TestB'], []]]:
            with self.subTest(groups=groups), self.assertRaises(ValueError):
                race.validate_partition(['TestA', 'TestB'], groups)

    def test_execution_checks_both_start_and_completion_with_no_extra_tests(self):
        with tempfile.TemporaryDirectory() as temp:
            log = Path(temp) / 'execution.jsonl'
            valid = [{'Action': a, 'Test': n} for n in ['TestA', 'Example', 'FuzzSeed']
                     for a in ['run', 'pass']]
            valid += [{'Action': 'run', 'Test': 'FuzzSeed/seed#0'}]
            def write(events):
                log.write_text(''.join(json.dumps(e) + '\n' for e in events))
            write(valid)
            race.verify_execution(log, ['TestA', 'Example', 'FuzzSeed'])
            for events in [valid[1:], valid[:-2], valid + valid[:2],
                           valid + [{'Action': 'run', 'Test': 'TestExtra'}]]:
                write(events)
                with self.assertRaises(ValueError):
                    race.verify_execution(log, ['TestA', 'Example', 'FuzzSeed'])

    def test_failure_and_signal_are_not_hidden_by_successful_sibling(self):
        for program in ['import sys; print("fixture failure", flush=True); sys.exit(7)',
                        'import os, signal; os.kill(os.getpid(), signal.SIGTERM)']:
            with self.subTest(program=program), tempfile.TemporaryDirectory() as temp:
                jobs = [race.Job([sys.executable, '-c', code], temp, Path(temp) / str(i))
                        for i, code in enumerate([program, 'print("sibling passed")'])]
                try:
                    with self.assertRaises(RuntimeError):
                        race.wait_jobs(jobs, time.monotonic() + 5)
                    self.assertIn('sibling passed', jobs[1].log.read_text())
                finally:
                    for job in jobs:
                        job.stop()

    def test_shared_deadline_stops_owned_process_and_keeps_log(self):
        with tempfile.TemporaryDirectory() as temp:
            job = race.Job([sys.executable, '-u', '-c',
                            'import time; print("fixture started"); time.sleep(30)'],
                           temp, Path(temp) / 'slow.log', ['TestSlow'])
            try:
                with self.assertRaises(TimeoutError):
                    race.wait_jobs([job], time.monotonic() + 0.3)
            finally:
                job.stop()
            self.assertIsNotNone(job.process.poll())
            self.assertIn('fixture started', job.log.read_text())

    def test_native_go_examples_fuzz_and_test2json_failure(self):
        # A tiny standalone module verifies Go's real -list/-run/test2json contract.
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            (root / 'go.mod').write_text('module example.com/partitionfixture\n\ngo 1.24\n')
            (root / 'fixture_test.go').write_text('''package fixture
import ("fmt"; "os"; "testing")
func TestNew(t *testing.T) { if os.Getenv("CI_FIXTURE_FAIL") == "1" { t.Fatal("fictional failure") } }
func Example() { fmt.Println("fictional"); // Output: fictional
}
func FuzzSeed(f *testing.F) { f.Add("fictional"); f.Fuzz(func(t *testing.T, s string) {}) }
func BenchmarkCost(b *testing.B) { b.Fatal("benchmarks must not run") }
''')
            binary = str(root / 'fixture.test')
            subprocess.run(['go', 'test', '-race', '-c', '-o', binary], cwd=root, check=True)
            names = race.inventory(subprocess.check_output([binary, '-test.list=.'], text=True))
            self.assertCountEqual(names, ['TestNew', 'Example', 'FuzzSeed'])
            for i, group in enumerate([names[:1], names[1:]]):
                job = race.Job(['go', 'tool', 'test2json', '-t', binary, '-test.v=test2json',
                                '-test.run=' + race.expression(group)], root, root / f'{i}.jsonl', group)
                try:
                    race.wait_jobs([job], time.monotonic() + 30)
                finally:
                    job.stop()
            result = subprocess.run(['go', 'tool', 'test2json', binary, '-test.run=^TestNew$'],
                                    env={**race.os.environ, 'CI_FIXTURE_FAIL': '1'}, capture_output=True)
            self.assertNotEqual(result.returncode, 0)

    def test_windows_native_steps_all_obey_documentation_scope(self):
        # Catch a new unguarded diagnostic step, not just the known assignment miss.
        workflow = Path(__file__).resolve().parents[1] / '.github/workflows/checks.yml'
        text = workflow.read_text()
        for job in ['windows-acl', 'windows-processes']:
            block = re.search(r'^  ' + job + r':\n(.*?)(?=^  [a-z][a-z-]*:|\Z)', text, re.M | re.S).group(1)
            for step in re.split(r'^      - ', block, flags=re.M)[1:]:
                if re.search(r'go (test|vet) ', step):
                    self.assertIn("steps.scope.outputs.scope != 'docs'", step)


if __name__ == '__main__':
    unittest.main()
