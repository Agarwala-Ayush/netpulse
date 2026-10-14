// Command netpulse is a concurrent network health checker: DNS, HTTP, TLS
// certificate expiry and TCP latency checks across many hosts, with a CLI,
// a live terminal dashboard and a Prometheus exporter.
package main

import (
	"os"

	"github.com/agarwala-ayush/netpulse/internal/cli"
)

func main() {
	os.Exit(cli.Execute())
}
