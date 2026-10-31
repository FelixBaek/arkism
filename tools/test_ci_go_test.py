import socket
import unittest

from ci_go_test import take_slot


def free_ports(n):
    socks = [socket.socket() for _ in range(n)]
    for s in socks:
        s.bind(("127.0.0.1", 0))
    ports = [s.getsockname()[1] for s in socks]
    for s in socks:
        s.close()
    return ports


class SlotTests(unittest.TestCase):
    def test_slots_are_exclusive_and_freed_on_close(self):
        ports = free_ports(2)
        first = take_slot(ports, wait=0)
        second = take_slot(ports, wait=0)
        try:
            self.assertEqual(sorted([first.getsockname()[1], second.getsockname()[1]]), sorted(ports))
            self.assertIsNone(take_slot(ports, wait=0))  # full: gives up instead of blocking the job
            first.close()
            again = take_slot(ports, wait=0)
            self.assertIsNotNone(again)
            again.close()
        finally:
            first.close()
            second.close()


class SerialMeasurementTests(unittest.TestCase):
    def test_measurements_run_once_outside_parallel_pool_with_coverage(self):
        import tempfile
        from unittest.mock import Mock, patch
        import ci_go_test as ci
        names = ["TestOrdinary", *sorted(ci.SERIAL_TESTS["./internal/memory"])]
        pool = Mock()
        with tempfile.TemporaryDirectory() as tmp, patch.object(ci, "checked", side_effect=["", "\n".join(names)]):
            groups, serial = ci.start_groups("./internal/memory", 0, ["-race"], True, 6, tmp, pool)
        self.assertEqual(pool.submit.call_count, 1)
        self.assertEqual(len(groups), 1)
        self.assertEqual(len(serial), 1)
        ordinary = groups[0][2]
        deferred = serial[0][1]
        self.assertEqual(ordinary[2], "^(TestOrdinary)$")
        self.assertEqual(deferred[2], "^(" + "|".join(names[1:]) + ")$")
        self.assertEqual(ordinary[0], deferred[0])
        self.assertEqual(ordinary[3:], deferred[3:])
        self.assertEqual(groups[0][3], serial[0][2])
        self.assertIn("-test.count=1", deferred)
        self.assertIn("-test.timeout=10m", deferred)
        self.assertTrue(any(x.startswith("-test.gocoverdir=") for x in deferred))

    def test_main_waits_for_parallel_results_then_propagates_serial_failure(self):
        from unittest.mock import Mock, patch
        import ci_go_test as ci
        events = []
        def parallel(*args):
            events.append("parallel joined")
            return False
        def serial(*args):
            self.assertEqual(events, ["parallel joined"])
            events.append("serial ran")
            return True
        with patch("sys.argv", ["ci_go_test.py", "--coverage"]), \
                patch.object(ci, "take_slot", return_value=Mock()), \
                patch.object(ci, "HEAVY", ["./internal/memory"]), \
                patch.object(ci, "start_rest"), \
                patch.object(ci, "start_groups", return_value=(["parallel"], ["serial"])), \
                patch.object(ci, "report", side_effect=parallel), \
                patch.object(ci, "report_serial", side_effect=serial), \
                patch.object(ci, "merge_coverage") as merge:
            self.assertEqual(ci.main(), 1)
            merge.assert_not_called()
        self.assertEqual(events, ["parallel joined", "serial ran"])

    def test_serial_failure_is_not_rerun_or_downgraded(self):
        from unittest.mock import patch
        import ci_go_test as ci
        with patch.object(ci, "run", return_value=(1, "worker round dependency_unavailable")) as run, \
                patch.object(ci, "passes_alone") as rerun:
            self.assertTrue(ci.report_serial([("./internal/memory", ["test-binary"], None)]))
            run.assert_called_once()
            rerun.assert_not_called()


class SerialDaemonPackageTests(unittest.TestCase):
    def test_all_discovered_daemon_tests_stay_in_one_deferred_batch(self):
        import tempfile
        from unittest.mock import Mock, patch
        import ci_go_test as ci
        names = ["TestBefore", "TestDynamicsIntegrationOffShadowSameAnswerPG", "TestAfter", "FuzzSeed", "ExampleMain"]
        pool = Mock()
        with tempfile.TemporaryDirectory() as tmp, patch.object(ci, "checked", side_effect=["", "\n".join(names)]):
            groups, serial = ci.start_groups("./cmd/memoryd", 2, ["-race", "-tags=integration"], True, 6, tmp, pool)
        self.assertEqual(groups, [])
        pool.submit.assert_not_called()
        self.assertEqual(len(serial), 1)
        rel, cmd, env = serial[0]
        self.assertEqual(rel, "./cmd/memoryd")
        self.assertEqual(cmd[2], "^(" + "|".join(names) + ")$")
        self.assertIn("-test.count=1", cmd)
        self.assertIn("-test.timeout=10m", cmd)
        self.assertTrue(cmd[0].endswith("heavy2.test"))
        self.assertTrue(env["GOCOVERDIR"].endswith("cov2"))
        self.assertIn("-test.gocoverdir=" + env["GOCOVERDIR"], cmd)

    def test_rest_does_not_run_daemon_package_a_second_time(self):
        from unittest.mock import Mock, patch
        import ci_go_test as ci
        module = "github.com/FelixBaek/arkism"
        packages = [module + "/cmd/memoryd", module + "/cmd/memory-principal", module + "/internal/memory", module + "/internal/host/crew"]
        pool = Mock()
        with patch.object(ci, "checked", side_effect=["\n".join(packages), module]):
            ci.start_rest(["-race"], [], False, "/tmp", pool)
        command = pool.submit.call_args.args[1]
        self.assertNotIn(module + "/cmd/memoryd", command)
        self.assertIn(module + "/cmd/memory-principal", command)

    def test_coverage_merges_the_third_package(self):
        import tempfile
        from pathlib import Path
        from unittest.mock import patch
        import ci_go_test as ci
        with tempfile.TemporaryDirectory() as tmp:
            Path(tmp, "rest.out").write_text("mode: atomic\nrest.go:1 1 1\n")
            output = Path(tmp, "merged.out")
            def checked(command):
                self.assertEqual(next(x for x in command if x.startswith("-i=")), "-i=" + ",".join(str(Path(tmp, "cov" + str(i))) for i in range(3)))
                Path(tmp, "heavy.out").write_text("mode: atomic\ncmd/memoryd/main.go:1 1 1\n")
            with patch.object(ci, "checked", side_effect=checked), patch.object(ci, "COVERAGE_FILE", str(output)):
                ci.merge_coverage(tmp)
            self.assertEqual(output.read_text(), "mode: atomic\nrest.go:1 1 1\ncmd/memoryd/main.go:1 1 1\n")


if __name__ == "__main__":
    unittest.main()
