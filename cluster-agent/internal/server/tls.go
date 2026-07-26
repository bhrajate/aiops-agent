package server

// tls.go implements the mTLS server posture from docs/SECURITY.md §3.
//
// The Cluster Agent is the TLS server; the Tool Gateway (control-plane) is the
// client. When enabled, the agent requires and verifies a client certificate
// signed by the configured client CA (RequireAndVerifyClientCert). When
// disabled, it stays plaintext for local development.

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// TLSConfig holds the mTLS server settings sourced from AIOPS_AGENT_TLS_*.
type TLSConfig struct {
	Enabled  bool
	CertFile string
	KeyFile  string
	ClientCA string
}

// TLSConfigFromEnv reads the mTLS server configuration.
//
//	AIOPS_AGENT_TLS_ENABLED     enable mTLS (default false)
//	AIOPS_AGENT_TLS_CERT        server certificate (PEM)
//	AIOPS_AGENT_TLS_KEY         server private key (PEM)
//	AIOPS_AGENT_TLS_CLIENT_CA   CA bundle used to verify client certs (PEM)
func TLSConfigFromEnv() TLSConfig {
	enabled, _ := strconv.ParseBool(strings.TrimSpace(os.Getenv("AIOPS_AGENT_TLS_ENABLED")))
	return TLSConfig{
		Enabled:  enabled,
		CertFile: os.Getenv("AIOPS_AGENT_TLS_CERT"),
		KeyFile:  os.Getenv("AIOPS_AGENT_TLS_KEY"),
		ClientCA: os.Getenv("AIOPS_AGENT_TLS_CLIENT_CA"),
	}
}

// Build validates the configuration and returns a *tls.Config that requires and
// verifies client certificates. It returns nil (with nil error) when TLS is
// disabled, so callers fall back to plaintext ListenAndServe.
func (c TLSConfig) Build() (*tls.Config, error) {
	if !c.Enabled {
		return nil, nil
	}
	if c.CertFile == "" || c.KeyFile == "" {
		return nil, fmt.Errorf("mTLS enabled but AIOPS_AGENT_TLS_CERT / AIOPS_AGENT_TLS_KEY not set")
	}
	if c.ClientCA == "" {
		return nil, fmt.Errorf("mTLS enabled but AIOPS_AGENT_TLS_CLIENT_CA not set (client cert cannot be verified)")
	}

	cert, err := tls.LoadX509KeyPair(c.CertFile, c.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("load server keypair: %w", err)
	}

	caPEM, err := os.ReadFile(c.ClientCA)
	if err != nil {
		return nil, fmt.Errorf("read client CA: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("client CA %q contains no valid certificates", c.ClientCA)
	}

	return &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{cert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    pool,
	}, nil
}
