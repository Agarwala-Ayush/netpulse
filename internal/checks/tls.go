package checks

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"
)

// TLSConfig configures a TLS certificate check.
type TLSConfig struct {
	Address     string // host:port to connect to
	ServerName  string // SNI and hostname to verify; defaults to the address host
	WarnDays    int    // WARN when the chain expires within this many days
	CritDays    int    // FAIL when the chain expires within this many days
	LatencyWarn time.Duration
	Roots       *x509.CertPool // trust anchors; nil means the system trust store
}

// TLSChecker connects to a TLS endpoint and inspects the certificate chain.
type TLSChecker struct {
	cfg TLSConfig
	now func() time.Time // injectable clock for tests
}

// NewTLSChecker validates cfg and builds a checker.
func NewTLSChecker(cfg TLSConfig) (*TLSChecker, error) {
	host, _, err := net.SplitHostPort(cfg.Address)
	if err != nil {
		return nil, fmt.Errorf("tls: address must be host:port: %w", err)
	}
	if cfg.ServerName == "" {
		cfg.ServerName = host
	}
	if cfg.WarnDays <= 0 {
		cfg.WarnDays = 30
	}
	if cfg.CritDays <= 0 {
		cfg.CritDays = 7
	}
	return &TLSChecker{cfg: cfg, now: time.Now}, nil
}

// Kind implements Checker.
func (c *TLSChecker) Kind() Kind { return KindTLS }

// Check implements Checker.
//
// The handshake is done with InsecureSkipVerify and the chain is verified
// manually afterwards. That sounds backwards but is deliberate: with normal
// verification an expired or self-signed certificate aborts the handshake
// and we would learn nothing about it. Skipping verification during the
// handshake lets us always read the certificates, and then report both
// "is it trusted?" and "when does it expire?" independently.
func (c *TLSChecker) Check(ctx context.Context) Result {
	res := Result{Kind: KindTLS, Status: StatusOK}
	start := time.Now()

	var d net.Dialer
	raw, err := d.DialContext(ctx, "tcp", c.cfg.Address)
	connected := time.Now()
	if err != nil {
		res.Latency = connected.Sub(start)
		res.fail(fmt.Errorf("connect %s: %w", c.cfg.Address, unwrapOpError(err)))
		return res
	}
	defer raw.Close()

	conn := tls.Client(raw, &tls.Config{
		ServerName: c.cfg.ServerName,
		// Allow legacy versions so we can connect and *report* them as a
		// warning rather than failing with an opaque handshake error.
		MinVersion:         tls.VersionTLS10, //nolint:gosec // inspection only, no data exchanged
		InsecureSkipVerify: true,             //nolint:gosec // chain is verified manually below
	})
	if err := conn.HandshakeContext(ctx); err != nil {
		res.Latency = time.Since(start)
		res.Phases = []Phase{{Name: "connect", Duration: connected.Sub(start)}}
		res.fail(fmt.Errorf("tls handshake: %w", err))
		return res
	}
	handshaken := time.Now()
	res.Latency = handshaken.Sub(start)
	res.Phases = []Phase{
		{Name: "connect", Duration: connected.Sub(start)},
		{Name: "handshake", Duration: handshaken.Sub(connected)},
	}

	state := conn.ConnectionState()
	if len(state.PeerCertificates) == 0 {
		res.fail(errors.New("server presented no certificates"))
		return res
	}
	leaf := state.PeerCertificates[0]
	now := c.now()

	earliest := leaf.NotAfter
	for _, cert := range state.PeerCertificates[1:] {
		if cert.NotAfter.Before(earliest) {
			earliest = cert.NotAfter
		}
	}
	daysLeft := earliest.Sub(now).Hours() / 24

	verifyErr := c.verify(state.PeerCertificates, now)
	info := &TLSInfo{
		Subject:        subjectName(leaf),
		Issuer:         issuerName(leaf),
		DNSNames:       leaf.DNSNames,
		NotBefore:      leaf.NotBefore,
		NotAfter:       leaf.NotAfter,
		EarliestExpiry: earliest,
		DaysLeft:       daysLeft,
		Version:        tls.VersionName(state.Version),
		CipherSuite:    tls.CipherSuiteName(state.CipherSuite),
		ChainLength:    len(state.PeerCertificates),
		Verified:       verifyErr == nil,
	}
	res.TLS = info

	res.Summary = fmt.Sprintf("expires in %s (%s) · %s", humanDays(daysLeft),
		earliest.UTC().Format("2006-01-02"), info.Version)
	res.AddDetail("subject", info.Subject)
	res.AddDetail("issuer", info.Issuer)
	res.AddDetail("sans", Shorten(strings.Join(info.DNSNames, ", "), 120))
	res.AddDetail("valid from", leaf.NotBefore.UTC().Format(time.RFC3339))
	res.AddDetail("valid until", leaf.NotAfter.UTC().Format(time.RFC3339))
	if !earliest.Equal(leaf.NotAfter) {
		res.AddDetail("chain expires", earliest.UTC().Format(time.RFC3339))
	}
	res.AddDetail("version", info.Version)
	res.AddDetail("cipher", info.CipherSuite)
	res.AddDetail("chain length", fmt.Sprint(info.ChainLength))

	switch {
	case daysLeft < 0:
		res.fail(fmt.Errorf("certificate expired %s ago", humanDays(-daysLeft)))
		res.Summary = fmt.Sprintf("EXPIRED %s ago (%s)", humanDays(-daysLeft), earliest.UTC().Format("2006-01-02"))
	case verifyErr != nil:
		res.fail(verifyErr)
		res.Summary = "untrusted: " + Shorten(verifyErr.Error(), 70)
	case daysLeft < float64(c.cfg.CritDays):
		res.fail(fmt.Errorf("certificate expires in %s (critical threshold %dd)", humanDays(daysLeft), c.cfg.CritDays))
	case daysLeft < float64(c.cfg.WarnDays):
		res.warn(fmt.Sprintf("certificate expires in %s (warning threshold %dd)", humanDays(daysLeft), c.cfg.WarnDays))
	}
	if state.Version < tls.VersionTLS12 {
		res.warn(fmt.Sprintf("server negotiated deprecated %s", info.Version))
	}
	if c.cfg.LatencyWarn > 0 && res.Latency > c.cfg.LatencyWarn {
		res.warn(fmt.Sprintf("handshake took %s (threshold %s)",
			FormatDuration(res.Latency), FormatDuration(c.cfg.LatencyWarn)))
	}
	return res
}

// verify checks the presented chain against the system trust store and the
// expected hostname, exactly as a browser would.
func (c *TLSChecker) verify(chain []*x509.Certificate, now time.Time) error {
	intermediates := x509.NewCertPool()
	for _, cert := range chain[1:] {
		intermediates.AddCert(cert)
	}
	_, err := chain[0].Verify(x509.VerifyOptions{
		DNSName:       c.cfg.ServerName,
		Intermediates: intermediates,
		Roots:         c.cfg.Roots,
		CurrentTime:   now,
	})
	if err == nil {
		return nil
	}
	// Turn x509's typed errors into short, actionable messages.
	var (
		unknownAuth x509.UnknownAuthorityError
		hostErr     x509.HostnameError
		invalidErr  x509.CertificateInvalidError
	)
	switch {
	case errors.As(err, &hostErr):
		return fmt.Errorf("certificate not valid for %q", c.cfg.ServerName)
	case errors.As(err, &unknownAuth):
		return errors.New("certificate signed by unknown authority (self-signed or missing intermediate)")
	case errors.As(err, &invalidErr) && invalidErr.Reason == x509.Expired:
		return errors.New("certificate has expired or is not yet valid")
	}
	return err
}

func subjectName(c *x509.Certificate) string {
	if c.Subject.CommonName != "" {
		return c.Subject.CommonName
	}
	if len(c.DNSNames) > 0 {
		return c.DNSNames[0]
	}
	return c.Subject.String()
}

func issuerName(c *x509.Certificate) string {
	name := c.Issuer.CommonName
	if len(c.Issuer.Organization) > 0 {
		if name == "" {
			return c.Issuer.Organization[0]
		}
		return fmt.Sprintf("%s (%s)", name, c.Issuer.Organization[0])
	}
	if name == "" {
		return c.Issuer.String()
	}
	return name
}

// humanDays renders a day count compactly: "63d", "18h", "-2d".
func humanDays(days float64) string {
	if days < 1 && days > -1 {
		return fmt.Sprintf("%.0fh", days*24)
	}
	return fmt.Sprintf("%.0fd", days)
}

// unwrapOpError drops the "dial tcp 1.2.3.4:443:" prefix of *net.OpError.
func unwrapOpError(err error) error {
	var op *net.OpError
	if errors.As(err, &op) && op.Err != nil {
		return op.Err
	}
	return err
}
