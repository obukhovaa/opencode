package db

import (
	"crypto/tls"
	"crypto/x509"
	_ "embed"
	"sync"

	"github.com/go-sql-driver/mysql"

	"github.com/opencode-ai/opencode/internal/logging"
)

// AuroraTLSConfigName is the name under which the Amazon RDS / Aurora CA trust
// chain is registered with the go-sql-driver/mysql driver.
//
// A DSN opts into verified TLS against an Aurora / RDS endpoint by adding the
// parameter `tls=aurora`, e.g.
//
//	opencode_user:***@tcp(my-cluster.cluster-xxxx.eu-central-1.rds.amazonaws.com:3306)/opencode?parseTime=true&tls=aurora
//
// DSNs that omit the parameter — notably the self-hosted MySQL sidecar used in
// dev (`...@tcp(<svc>:3306)/opencode?parseTime=true`) — are completely
// unaffected and keep connecting exactly as before. This makes Aurora support
// a pure opt-in that is backwards compatible with the existing deployment.
const AuroraTLSConfigName = "aurora"

// rdsCABundle is the Amazon RDS certificate bundle for eu-central-1 (the region
// the c2 agent Aurora Serverless cluster lives in — see GENAI-48). It is the
// verbatim AWS bundle and contains the three self-signed regional root CAs
// (ECC384, RSA2048, RSA4096 G1). The Aurora endpoint sends its leaf plus the
// signing intermediate during the handshake, so trusting these roots is all
// that is required to verify the server certificate. Refresh it from
// https://truststore.pki.rds.amazonaws.com/eu-central-1/eu-central-1-bundle.pem
// if AWS rotates the CA.
//
//go:embed assets/rds-eu-central-1-bundle.pem
var rdsCABundle []byte

var registerAuroraTLSOnce sync.Once

// registerAuroraTLS registers a TLS config named "aurora" (AuroraTLSConfigName)
// that trusts the Amazon RDS/Aurora certificate authorities in addition to the
// host's system roots. It is safe — and cheap — to call on every Connect; the
// registration itself happens exactly once.
//
// ServerName is intentionally left empty: go-sql-driver clones the registered
// config per connection and fills ServerName from the DSN host when it is
// blank, so this one shared config verifies whichever Aurora endpoint the DSN
// points at.
func registerAuroraTLS() {
	registerAuroraTLSOnce.Do(func() {
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(rdsCABundle) {
			logging.Error("mysql: failed to append embedded RDS CA bundle; DSNs using tls=aurora will fail to verify the server certificate")
			return
		}
		if err := mysql.RegisterTLSConfig(AuroraTLSConfigName, &tls.Config{
			RootCAs:    pool,
			MinVersion: tls.VersionTLS12,
		}); err != nil {
			logging.Error("mysql: failed to register Aurora TLS config", "error", err)
			return
		}
		logging.Debug("mysql: registered Aurora TLS config (available via DSN tls=aurora)")
	})
}
