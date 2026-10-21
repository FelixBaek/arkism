import unittest

from check_core_coverage import TARGETS
from check_package_coverage import enforce, package_totals


class PackageCoverageTests(unittest.TestCase):
    def profile(self):
        lines = ["mode: atomic"]
        for name in TARGETS:
            lines.append(f"github.com/FelixBaek/arkism/{name}:1.1,2.2 9 1")
            lines.append(f"github.com/FelixBaek/arkism/{name}:3.1,4.2 1 0")
        lines.append("github.com/FelixBaek/arkism/internal/crewrun/run.go:1.1,2.2 9 1")
        lines.append("github.com/FelixBaek/arkism/internal/crewrun/run.go:3.1,4.2 1 0")
        # Similar names cannot improve the package's coverage.
        lines.append("github.com/FelixBaek/arkism/internal/crewrunner/x.go:1.1,2.2 1000 1")
        return "\n".join(lines) + "\n"

    def test_exact_floor_and_one_statement_regression(self):
        floors = {"internal/memory": 90, "internal/crewrun": 90}
        totals = package_totals(self.profile(), floors)
        self.assertEqual(totals, {"internal/memory": [27, 30], "internal/crewrun": [9, 10]})
        self.assertEqual(enforce(totals, floors), [])
        totals["internal/crewrun"][0] = 8
        self.assertEqual(enforce(totals, floors), ["internal/crewrun"])

    def test_missing_package_and_corrupt_profiles_are_refused(self):
        profile = self.profile()
        with self.assertRaises(ValueError):
            package_totals(profile, ["internal/missing"])
        duplicated = profile + profile.splitlines()[1] + "\n"
        with self.assertRaises(ValueError):
            package_totals(duplicated, ["internal/memory"])
        for floor in [0, -1, 101, "90", True]:
            with self.subTest(floor=floor), self.assertRaises(ValueError):
                enforce({"internal/memory": [9, 10]}, {"internal/memory": floor})


if __name__ == "__main__":
    unittest.main()
