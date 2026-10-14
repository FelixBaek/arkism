package crewrun

import "testing"

func TestLoopbackAddr(t *testing.T) {
	for addr, want := range map[string]bool{
		"127.0.0.1:8095": true, "localhost:8095": true, "[::1]:8095": true, "127.0.0.2:25": true,
		"0.0.0.0:8095": false, ":8095": false, "10.0.0.5:8095": false, "smtp.example.com:587": false, "127.0.0.1": false,
	} {
		if got := loopbackAddr(addr); got != want {
			t.Errorf("loopbackAddr(%q) = %v, want %v", addr, got, want)
		}
	}
}
