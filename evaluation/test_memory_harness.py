"""Collect the fixed baseline, system and experiment harness tests."""
import importlib.util
from pathlib import Path
import sys
import unittest

_MODULES = ('test_contract_io.py', 'test_fixture_gen.py', 'test_metrics.py',
            'test_report.py', 'test_acceptance.py')
_SYSTEM_MODULES = ('system/sys_01/test_shadow_contract.py',)
_EXPERIMENT_MODULES = ('experiments/exp_01/test_protocol.py',)
_DIRECTORY = Path(__file__).parent / 'memory-harness-v1'


class _RequiredResult:
    def __init__(self, result):
        self.result = result

    def __getattr__(self, name):
        return getattr(self.result, name)

    def addExpectedFailure(self, test, error):
        failure = AssertionError('required harness test did not pass')
        self.result.addFailure(test, (AssertionError, failure, None))

    def addSkip(self, test, reason):
        # A required skip is missing verification, never a successful collection.
        error = AssertionError('required harness test skipped')
        self.result.addFailure(test, (AssertionError, error, None))


class _RequiredSuite(unittest.TestSuite):
    def run(self, result, debug=False):
        return super().run(_RequiredResult(result), debug)


def load_tests(loader, standard_tests, pattern):
    suite = _RequiredSuite()
    original_path = list(sys.path)
    try:
        sys.path.insert(0, str(_DIRECTORY))
        for filename in _MODULES + _SYSTEM_MODULES + _EXPERIMENT_MODULES:
            path = _DIRECTORY / filename
            if not path.is_file() or path.is_symlink():
                raise AssertionError('required harness test module missing')
            name = 'arkism_memory_harness_collected_' + path.stem
            spec = importlib.util.spec_from_file_location(name, path)
            module = importlib.util.module_from_spec(spec)
            sys.modules[name] = module
            try:
                spec.loader.exec_module(module)
            except unittest.SkipTest:
                raise AssertionError('required harness test module skipped') from None
            if getattr(module, '__unittest_skip__', False):
                raise AssertionError('required harness test module skipped')
            tests = loader.loadTestsFromModule(module)
            if tests.countTestCases() == 0:
                raise AssertionError('required harness test module is empty')
            suite.addTests(tests)
    finally:
        sys.path[:] = original_path
    if suite.countTestCases() == 0:
        raise AssertionError('required harness suite is empty')
    return suite
