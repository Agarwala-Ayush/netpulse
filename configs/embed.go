// Package configs embeds the example configuration so that `netpulse init`
// can write it out from a single self-contained binary.
package configs

import _ "embed"

// Example is the annotated example configuration.
//
//go:embed netpulse.example.yaml
var Example []byte
