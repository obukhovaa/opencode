package db

import (
	"crypto/x509"
	"encoding/pem"
	"strings"
	"testing"

	"github.com/go-sql-driver/mysql"
)

// TestRegisterAuroraTLS verifies that after registration a DSN using
// `tls=aurora` resolves to a TLS config backed by the embedded RDS CA bundle.
func TestRegisterAuroraTLS(t *testing.T) {
	registerAuroraTLS()

	cfg, err := mysql.ParseDSN("opencode_user:pw@tcp(cluster.cluster-abc.eu-central-1.rds.amazonaws.com:3306)/opencode?parseTime=true&tls=aurora")
	if err != nil {
		t.Fatalf("DSN with tls=aurora should parse after registration: %v", err)
	}
	if cfg.TLS == nil {
		t.Fatal("expected a resolved TLS config for tls=aurora")
	}
	if cfg.TLS.RootCAs == nil {
		t.Fatal("expected RootCAs populated from the embedded RDS bundle")
	}
	// The shared config is registered with an empty ServerName; the driver
	// clones it and fills ServerName from the DSN host, so the same config
	// verifies whichever Aurora endpoint a DSN points at.
	if want := "cluster.cluster-abc.eu-central-1.rds.amazonaws.com"; cfg.TLS.ServerName != want {
		t.Fatalf("expected ServerName derived from DSN host %q, got %q", want, cfg.TLS.ServerName)
	}
}

// TestSelfHostedDSNUnaffected guards the backwards-compat requirement: a DSN
// without the tls parameter (the self-hosted MySQL sidecar) must remain a
// plaintext connection even after the aurora config is registered.
func TestSelfHostedDSNUnaffected(t *testing.T) {
	registerAuroraTLS()

	cfg, err := mysql.ParseDSN("opencode_user:pw@tcp(release-c2-agent-mysql:3306)/opencode?parseTime=true")
	if err != nil {
		t.Fatalf("self-hosted DSN should parse: %v", err)
	}
	if cfg.TLS != nil || cfg.TLSConfig != "" {
		t.Fatalf("self-hosted DSN must stay plaintext, got TLSConfig=%q", cfg.TLSConfig)
	}
}

// TestEmbeddedBundleIsGlobal guards against regressing to a regional bundle:
// agents verify Aurora endpoints in every region Piano runs them in.
func TestEmbeddedBundleIsGlobal(t *testing.T) {
	regions := map[string]bool{"eu-central-1": false, "us-east-1": false}
	rest := rdsCABundle
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			t.Fatalf("embedded bundle holds an unparsable certificate: %v", err)
		}
		for region := range regions {
			if strings.Contains(cert.Subject.CommonName, region) {
				regions[region] = true
			}
		}
	}
	for region, seen := range regions {
		if !seen {
			t.Fatalf("embedded RDS bundle has no root CA for %s; it must be the global bundle", region)
		}
	}
}
