"""Offline regression checks for evaluation artifact and executable boundaries."""
import importlib.util
import pathlib
import tempfile
import unittest


ROOT = pathlib.Path(__file__).resolve().parent


def load(relative):
    spec = importlib.util.spec_from_file_location('evaluation_fixture', ROOT / relative)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class PrivatePathsTest(unittest.TestCase):
    def test_live_paths_reject_shared_roots_and_symlink_escape(self):
        module = load('reuse-20261015/live_agents.py')
        with tempfile.TemporaryDirectory() as directory:
            base = pathlib.Path(directory)
            module.PRIVATE_ROOT = base / 'private'
            inside = module.PRIVATE_ROOT / 'state.json'
            self.assertEqual(module.confined_path(inside), inside)
            with self.assertRaises(ValueError):
                module.confined_path(base / 'outside.json')
            (module.PRIVATE_ROOT / 'escape').symlink_to(base, target_is_directory=True)
            with self.assertRaises(ValueError):
                module.confined_path(module.PRIVATE_ROOT / 'escape' / 'outside.json')
            module.PRIVATE_ROOT.chmod(0o777)
            with self.assertRaises(ValueError):
                module.confined_path(inside)

    def test_live_private_root_cannot_be_symlink(self):
        module = load('reuse-20261015/live_agents.py')
        with tempfile.TemporaryDirectory() as directory:
            base = pathlib.Path(directory)
            target = base / 'target'
            target.mkdir(mode=0o700)
            module.PRIVATE_ROOT = base / 'private'
            module.PRIVATE_ROOT.symlink_to(target, target_is_directory=True)
            with self.assertRaises(ValueError):
                module.confined_path(module.PRIVATE_ROOT / 'state.json')

    def test_exporter_is_private_owned_executable_not_symlink(self):
        module = load('reuse-v010/benchmark/run.py')
        with tempfile.TemporaryDirectory() as directory:
            module.PRIVATE_ROOT = pathlib.Path(directory) / 'private'
            module.EXPORTER = str(module.PRIVATE_ROOT / 'exporter.test')
            module.private_directory(module.PRIVATE_ROOT)
            executable = pathlib.Path(module.EXPORTER)
            executable.write_text('#!/bin/sh\nexit 0\n')
            executable.chmod(0o700)
            module.validate_exporter()
            executable.chmod(0o777)
            with self.assertRaises(ValueError):
                module.validate_exporter()
            executable.unlink()
            executable.symlink_to('/bin/sh')
            with self.assertRaises(ValueError):
                module.validate_exporter()
            module.PRIVATE_ROOT.chmod(0o777)
            with self.assertRaises(ValueError):
                module.validate_exporter()


if __name__ == '__main__':
    unittest.main()
