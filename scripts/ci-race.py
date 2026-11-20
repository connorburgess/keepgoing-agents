#!/usr/bin/env python3
"""Run the native race suite, with two disjoint command partitions per runner."""
import collections
import json
import os
from pathlib import Path
import re
import signal
import subprocess
import sys
import tempfile
import time

COMMAND = 'github.com/looptender/looptender/cmd/looptender'
# Source-only checks: the timeout guard failed after 330s in run 44399774595.
# Run these once, before the lifecycle suites, with the same race binary.
EARLY = {'TestDockHiddenTabReadsNothingG1', 'TestDockReadsTimeOutG2',
         'TestDockQuotaOnlyReadsAfterExplicitReview'}


def inventory(output):
    names = []
    for name in output.splitlines():
        if re.fullmatch(r'(Test|Example|Fuzz)\w*', name):
            names.append(name)
        elif not re.fullmatch(r'Benchmark\w+', name):
            raise ValueError(f'unexpected test inventory line: {name!r}')
    if not names or len(names) != len(set(names)):
        raise ValueError('empty or duplicate test inventory')
    return names


def validate_partition(names, groups):
    if collections.Counter(names) != collections.Counter(n for g in groups for n in g):
        raise ValueError('partition omits or duplicates inventory entries')
    if any(not g for g in groups):
        raise ValueError('empty partition')


def partition(names):
    if not EARLY <= set(names):
        raise ValueError('early guard missing from native inventory')
    early = sorted(EARLY)
    rest = sorted(set(names) - EARLY)
    groups = [early, rest[::2], rest[1::2]]
    validate_partition(names, groups)
    return groups


def expression(names):
    return '^(' + '|'.join(re.escape(n) for n in names) + ')$'


def verify_execution(path, names):
    started, ended = [], []
    with open(path) as stream:
        for line in stream:
            event = json.loads(line)
            name = event.get('Test', '')
            if not name or '/' in name:
                continue
            if event['Action'] == 'run':
                started.append(name)
            if event['Action'] in ('pass', 'skip', 'fail'):
                ended.append(name)
    expected = collections.Counter(names)
    if collections.Counter(started) != expected or collections.Counter(ended) != expected:
        raise ValueError(f'{path}: executed tests differ from selected inventory')


class Job:
    def __init__(self, argv, cwd, log, names=None):
        self.log, self.names = log, names
        self.started = time.monotonic()
        self.stopped = False
        self.output = open(log, 'w')
        try:
            self.process = subprocess.Popen(argv, cwd=cwd, stdout=self.output,
                                            stderr=subprocess.STDOUT, start_new_session=True)
        except BaseException:
            self.output.close()
            raise

    def finish(self):
        code = self.process.wait()
        self.output.close()
        seconds = round(time.monotonic() - self.started, 2)
        print(f'{self.log.name}: {seconds}s, exit {code}', flush=True)
        if code:
            raise RuntimeError(f'{self.log.name} failed ({code})')
        if self.names is not None:
            verify_execution(self.log, self.names)

    def stop(self, grace=5):
        if self.stopped:
            return
        # The leader can exit while a descendant still owns this process group.
        # Check the group itself, never a PID supplied by a test fixture.
        def group_alive():
            # Reap a dead leader before probing: Darwin can return EPERM for
            # a process group containing only that unreaped zombie.
            self.process.poll()
            try:
                os.killpg(self.process.pid, 0)
                return True
            except ProcessLookupError:
                return False

        # Defer cancellation until this owned group is gone. A repeated signal
        # must not mark incomplete cleanup done or strand a sibling process.
        old_mask = signal.pthread_sigmask(signal.SIG_BLOCK, {signal.SIGINT, signal.SIGTERM})
        try:
            if group_alive():
                try:
                    os.killpg(self.process.pid, signal.SIGTERM)
                except ProcessLookupError:
                    pass
                until = time.monotonic() + grace
                while group_alive() and time.monotonic() < until:
                    self.process.poll()  # reap the leader, independently of descendants
                    time.sleep(0.02)
                if group_alive():
                    try:
                        os.killpg(self.process.pid, signal.SIGKILL)
                    except ProcessLookupError:
                        pass
            self.process.wait()
            self.stopped = True
        finally:
            self.output.close()
            signal.pthread_sigmask(signal.SIG_SETMASK, old_mask)


def wait_jobs(jobs, deadline):
    pending = list(jobs)
    errors = []
    while pending:
        for job in pending[:]:
            if job.process.poll() is not None:
                pending.remove(job)
                try:
                    job.finish()
                except Exception as error:
                    errors.append(str(error))
                finally:
                    # Release ownership now; a later idempotent cleanup must not
                    # signal a recycled group identifier from an earlier guard.
                    job.stop()
        # Only the command partitions share the package's original 10m budget.
        # Other packages retain go test's own default per-package timeout.
        if any(j.names is not None for j in pending) and time.monotonic() >= deadline:
            raise TimeoutError('command package exhausted its 10-minute budget')
        if pending:
            time.sleep(0.1)
    if errors:
        raise RuntimeError('; '.join(errors))


def cleanup_jobs(jobs):
    # The first cancellation already failed the run. Ignore repeats while
    # cleaning every sibling and publishing all available logs.
    previous = {sig: signal.signal(sig, signal.SIG_IGN)
                for sig in (signal.SIGINT, signal.SIGTERM)}
    errors = []
    try:
        for job in jobs:
            try:
                job.stop()
            except Exception as error:
                errors.append(f'{job.log.name}: {error}')
        for job in jobs:
            try:
                print(f'::group::{job.log.name}', flush=True)
                print(job.log.read_text(), flush=True)
                print('::endgroup::', flush=True)
            except Exception as error:
                errors.append(f'{job.log.name} log: {error}')
    finally:
        for sig, handler in previous.items():
            signal.signal(sig, handler)
    if errors:
        raise RuntimeError('; '.join(errors))


def main():
    root = Path(__file__).resolve().parents[1]
    # Retain JSON inventory and complete logs, including on cancellation/failure.
    output = Path(os.environ.get('RUNNER_TEMP', tempfile.gettempdir())) / 'ci-race'
    output.mkdir(exist_ok=True)
    with tempfile.TemporaryDirectory(prefix='ci-race-build-') as build:
        binary = str(Path(build) / 'command.test')
        subprocess.run(['go', 'test', '-race', '-c', '-o', binary, './cmd/looptender'],
                       cwd=root, check=True)
        listing = subprocess.check_output([binary, '-test.list=.'],
                                         cwd=root / 'cmd/looptender', text=True)
        groups = partition(inventory(listing))
        (output / 'inventory.json').write_text(json.dumps(groups, indent=2) + '\n')
        packages = subprocess.check_output(['go', 'list', './...'], cwd=root, text=True).splitlines()
        if packages.count(COMMAND) != 1:
            raise ValueError('command package missing or duplicated')
        others = [p for p in packages if p != COMMAND]
        # One shared 10-minute execution budget, not ten minutes per partition.
        deadline = time.monotonic() + 600

        jobs = []

        def command(index):
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                raise TimeoutError('command package exhausted its 10-minute budget')
            log = output / f'command-{index}.jsonl'
            argv = ['go', 'tool', 'test2json', '-t', '-p', COMMAND, binary,
                    '-test.v=test2json', '-test.count=1',
                    f'-test.timeout={remaining:.3f}s', '-test.run=' + expression(groups[index])]
            job = Job(argv, root / 'cmd/looptender', log, groups[index])
            jobs.append(job)
            return job

        def interrupted(signum, frame):
            raise InterruptedError(f'received signal {signum}')

        previous = {sig: signal.signal(sig, interrupted) for sig in (signal.SIGINT, signal.SIGTERM)}
        try:
            wait_jobs([command(0)], deadline)
            command(1)
            command(2)
            jobs.append(Job(['go', 'test', '-race', *others], root, output / 'packages.log'))
            wait_jobs(jobs[1:], deadline)
        finally:
            try:
                cleanup_jobs(jobs)
            finally:
                for sig, handler in previous.items():
                    signal.signal(sig, handler)
        print('Native race inventory executed exactly once across command partitions.', flush=True)


if __name__ == '__main__':
    main()
