import os
from pathlib import Path
import tempfile
import unittest

from operator_files import input_file


class OperatorFileBoundaryTests(unittest.TestCase):
    def test_cli_paths_cannot_escape_approved_folder(self):
        with tempfile.TemporaryDirectory(prefix="arkism-operator-boundary-") as directory:
            base=Path(directory);root=base/"approved";root.mkdir(mode=0o700)
            inside=root/"token";inside.write_text("inert fixture");inside.chmod(0o600)
            outside=base/"outside";outside.write_text("must not be read")
            self.assertEqual(input_file("token",root,4096,private=True),inside)
            for selected in (outside,"../outside",root/"../outside"):
                with self.assertRaises(ValueError):
                    input_file(selected,root,4096)
            if os.name!="nt":
                (root/"link").symlink_to(outside)
                with self.assertRaises(ValueError):
                    input_file("link",root,4096)
    def test_private_mode_and_size_budgets_are_enforced(self):
        with tempfile.TemporaryDirectory(prefix="arkism-operator-size-") as directory:
            root=Path(directory);selected=root/"input";selected.write_text("four")
            with self.assertRaises(ValueError):
                input_file(selected,root,3)
            if os.name!="nt":
                selected.chmod(0o644)
                with self.assertRaises(ValueError):
                    input_file(selected,root,4096,private=True)
