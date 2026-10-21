import os
from pathlib import Path
import tempfile
import unittest

from ci_inputs import read_ci_text


class CIInputTests(unittest.TestCase):
    def test_inside_input_and_outside_or_oversized_input(self):
        with tempfile.TemporaryDirectory() as directory:
            parent = Path(directory)
            root = parent / "allowed"
            root.mkdir()
            good = root / "cases.json"
            good.write_bytes(b'\xef\xbb\xbf{"items":[]}')
            self.assertEqual(read_ci_text(good, [root]), '{"items":[]}')
            outside = parent / "private.json"
            outside.write_text("must not be read")
            for name in (outside, root / ".." / "private.json"):
                with self.assertRaises(ValueError):
                    read_ci_text(name, [root])
            with self.assertRaises(ValueError):
                read_ci_text(good, [root], 3)

    def test_link_escape_private_files_and_nonfiles_are_refused(self):
        with tempfile.TemporaryDirectory() as directory:
            parent = Path(directory)
            root = parent / "allowed"
            root.mkdir()
            private = root / ".env"
            private.write_text("inert private fixture")
            for name in (private, root):
                with self.assertRaises(ValueError):
                    read_ci_text(name, [root])
            if os.name != "nt":
                outside = parent / "outside.json"
                outside.write_text("inert outside fixture")
                link = root / "link.json"
                link.symlink_to(outside)
                with self.assertRaises(ValueError):
                    read_ci_text(link, [root])
