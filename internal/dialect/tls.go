package dialect

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"

	"github.com/iagapie/quire/internal/certs"
	"github.com/iagapie/quire/internal/profile"
)

// TLSConfig builds a TLS configuration from the profile settings for host. It returns
// nil when TLS is disabled. "prefer" and "require" encrypt without verifying the
// server certificate (as libpq does); "verify-ca" checks the chain and "verify-full"
// also the host name.
func TLSConfig(t profile.TLS, host string) (*tls.Config, error) {
	if t.Mode == profile.TLSDisable || t.Mode == "" {
		return nil, nil
	}
	tc := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: host}
	if t.ServerName != "" {
		tc.ServerName = t.ServerName
	}
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if t.UseAWSBundle {
		pool.AppendCertsFromPEM(certs.AWSGlobalBundle)
	}
	if t.CAFile != "" {
		pem, err := os.ReadFile(t.CAFile)
		if err != nil {
			return nil, fmt.Errorf("read CA file: %w", err)
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, errors.New("CA file contains no certificates")
		}
	}
	tc.RootCAs = pool
	if t.CertFile != "" || t.KeyFile != "" {
		cert, err := tls.LoadX509KeyPair(t.CertFile, t.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("client certificate: %w", err)
		}
		tc.Certificates = []tls.Certificate{cert}
	}
	switch t.Mode {
	case profile.TLSVerifyFull:
	case profile.TLSVerifyCA:
		tc.InsecureSkipVerify = true // the chain is verified below, without the host name
		tc.VerifyConnection = func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errors.New("server sent no certificate")
			}
			opts := x509.VerifyOptions{Roots: tc.RootCAs, Intermediates: x509.NewCertPool()}
			for _, c := range cs.PeerCertificates[1:] {
				opts.Intermediates.AddCert(c)
			}
			_, err := cs.PeerCertificates[0].Verify(opts)
			return err
		}
	default:
		tc.InsecureSkipVerify = true // explicit choice of an unverified mode (prefer/require)
	}
	return tc, nil
}
