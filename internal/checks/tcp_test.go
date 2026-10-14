package checks

import (
	"context"
	"net"
	"testing"
	"time"
)

// closedPort returns a loopback address on which nothing is listening.
func closedPort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

func TestTCPCheckSuccess(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()

	c, err := NewTCPChecker(TCPConfig{Address: ln.Addr().String(), Count: 5, ProbeInterval: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	res := c.Check(context.Background())
	if res.Status != StatusOK {
		t.Fatalf("status %s: %s", res.Status, res.Error)
	}
	if res.TCP.Sent != 5 || res.TCP.Received != 5 || res.TCP.Loss != 0 {
		t.Fatalf("unexpected stats %+v", res.TCP)
	}
	if res.TCP.Min > res.TCP.Avg || res.TCP.Avg > res.TCP.Max {
		t.Fatalf("min/avg/max out of order: %+v", res.TCP)
	}
}

func TestTCPCheckRefused(t *testing.T) {
	c, _ := NewTCPChecker(TCPConfig{Address: closedPort(t), Count: 2})
	res := c.Check(context.Background())
	if res.Status != StatusFail || res.TCP.Loss != 1 {
		t.Fatalf("want 100%% loss failure, got %s loss=%v", res.Status, res.TCP.Loss)
	}
}

func TestTCPCheckResolveFailure(t *testing.T) {
	c, _ := NewTCPChecker(TCPConfig{Address: "does-not-exist.invalid:443"})
	res := c.Check(context.Background())
	if res.Status != StatusFail {
		t.Fatalf("want failure for unresolvable host, got %s", res.Status)
	}
}

func TestRTTStats(t *testing.T) {
	ms := time.Millisecond
	minD, avg, maxD, jitter := rttStats([]time.Duration{10 * ms, 20 * ms, 30 * ms})
	if minD != 10*ms || avg != 20*ms || maxD != 30*ms {
		t.Fatalf("got min=%v avg=%v max=%v", minD, avg, maxD)
	}
	// population std-dev of {10,20,30} = sqrt(200/3) ≈ 8.165ms
	if jitter < 8*ms || jitter > 8200*time.Microsecond {
		t.Fatalf("jitter = %v, want ≈8.16ms", jitter)
	}
}

func TestPickAddrPrefersIPv4(t *testing.T) {
	addrs := []net.IPAddr{{IP: net.ParseIP("2001:db8::1")}, {IP: net.ParseIP("192.0.2.1")}}
	if got := pickAddr(addrs).String(); got != "192.0.2.1" {
		t.Fatalf("got %s, want IPv4", got)
	}
}
