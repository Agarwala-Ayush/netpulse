package checks

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/miekg/dns"
)

// startDNSServer runs an in-process authoritative DNS server on loopback so
// DNS checks can be tested without touching the network.
func startDNSServer(t *testing.T) string {
	t.Helper()
	zone := map[string][]string{
		"netpulse.test.":           {"netpulse.test. 300 IN A 192.0.2.10", "netpulse.test. 300 IN A 192.0.2.11"},
		"www.netpulse.test.":       {"www.netpulse.test. 60 IN CNAME netpulse.test."},
		"10.2.0.192.in-addr.arpa.": {"10.2.0.192.in-addr.arpa. 300 IN PTR netpulse.test."},
	}
	mux := dns.NewServeMux()
	mux.HandleFunc(".", func(w dns.ResponseWriter, req *dns.Msg) {
		m := new(dns.Msg)
		m.SetReply(req)
		q := req.Question[0]
		records, ok := zone[q.Name]
		if !ok {
			m.Rcode = dns.RcodeNameError // NXDOMAIN
		}
		for _, s := range records {
			rr, err := dns.NewRR(s)
			if err == nil && (rr.Header().Rrtype == q.Qtype || rr.Header().Rrtype == dns.TypeCNAME) {
				m.Answer = append(m.Answer, rr)
			}
		}
		_ = w.WriteMsg(m)
	})

	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &dns.Server{PacketConn: pc, Handler: mux}
	started := make(chan struct{})
	srv.NotifyStartedFunc = func() { close(started) }
	go func() { _ = srv.ActivateAndServe() }()
	<-started
	t.Cleanup(func() { _ = srv.Shutdown() })
	return pc.LocalAddr().String()
}

func TestDNSCheck(t *testing.T) {
	server := startDNSServer(t)

	tests := []struct {
		name    string
		cfg     DNSConfig
		status  Status
		records int
		errHas  string
	}{
		{"A records", DNSConfig{Host: "netpulse.test", Types: []string{"A"}}, StatusOK, 2, ""},
		{"NODATA for AAAA is fine when A exists", DNSConfig{Host: "netpulse.test", Types: []string{"A", "AAAA"}}, StatusOK, 2, ""},
		{"only NODATA fails", DNSConfig{Host: "netpulse.test", Types: []string{"AAAA"}}, StatusFail, 0, "no records"},
		{"NXDOMAIN fails", DNSConfig{Host: "missing.netpulse.test"}, StatusFail, 0, "NXDOMAIN"},
		{"CNAME", DNSConfig{Host: "www.netpulse.test", Types: []string{"A"}}, StatusOK, 1, ""},
		{"expect satisfied", DNSConfig{Host: "netpulse.test", Types: []string{"A"}, Expect: []string{"192.0.2.11"}}, StatusOK, 2, ""},
		{"expect missing", DNSConfig{Host: "netpulse.test", Types: []string{"A"}, Expect: []string{"198.51.100.1"}}, StatusFail, 2, "expected record"},
		{"reverse lookup for IP", DNSConfig{Host: "192.0.2.10"}, StatusOK, 1, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tc.cfg.Server = server
			c, err := NewDNSChecker(tc.cfg)
			if err != nil {
				t.Fatal(err)
			}
			res := c.Check(context.Background())
			if res.Status != tc.status {
				t.Fatalf("status = %s, want %s (error %q)", res.Status, tc.status, res.Error)
			}
			if got := len(res.DNS.Records); got != tc.records {
				t.Errorf("records = %d, want %d: %+v", got, tc.records, res.DNS.Records)
			}
			if tc.errHas != "" && !strings.Contains(res.Error, tc.errHas) {
				t.Errorf("error %q does not contain %q", res.Error, tc.errHas)
			}
		})
	}
}

func TestDNSCheckUnreachableServer(t *testing.T) {
	// Nothing listens here; UDP gets ICMP port-unreachable or times out.
	c, _ := NewDNSChecker(DNSConfig{Host: "netpulse.test", Server: closedUDPPort(t), Timeout: 200 * time.Millisecond})
	res := c.Check(context.Background())
	if res.Status != StatusFail {
		t.Fatalf("want failure, got %s", res.Status)
	}
}

func TestNewDNSCheckerValidation(t *testing.T) {
	if _, err := NewDNSChecker(DNSConfig{Host: "x", Types: []string{"BOGUS"}}); err == nil {
		t.Fatal("expected error for unknown record type")
	}
	if _, err := NewDNSChecker(DNSConfig{}); err == nil {
		t.Fatal("expected error for empty host")
	}
	c, err := NewDNSChecker(DNSConfig{Host: "x", Server: "9.9.9.9"})
	if err != nil || c.server != "9.9.9.9:53" {
		t.Fatalf("server without port should default to :53, got %q (%v)", c.server, err)
	}
}

func closedUDPPort(t *testing.T) string {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := pc.LocalAddr().String()
	_ = pc.Close()
	return addr
}
