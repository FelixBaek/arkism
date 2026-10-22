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


if __name__ == "__main__":
    unittest.main()
