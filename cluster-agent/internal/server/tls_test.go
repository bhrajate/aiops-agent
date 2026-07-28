package server

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aiops/cluster-agent/internal/datasource"
	"github.com/aiops/cluster-agent/internal/tools"
)

func TestTLSConfigDisabledByDefault(t *testing.T) {
	cfg, err := TLSConfig{Enabled: false}.Build()
	if err != nil || cfg != nil {
		t.Fatalf("disabled TLS should yield (nil,nil): cfg=%v err=%v", cfg, err)
	}
}

func TestTLSConfigMissingFiles(t *testing.T) {
	_, err := TLSConfig{Enabled: true}.Build()
	if err == nil {
		t.Fatal("expected error when cert/key missing")
	}
}

func TestTLSConfigFromEnv(t *testing.T) {
	t.Setenv("AIOPS_AGENT_TLS_ENABLED", "true")
	t.Setenv("AIOPS_AGENT_TLS_CERT", "/x/c")
	t.Setenv("AIOPS_AGENT_TLS_KEY", "/x/k")
	t.Setenv("AIOPS_AGENT_TLS_CLIENT_CA", "/x/ca")
	c := TLSConfigFromEnv()
	if !c.Enabled || c.CertFile != "/x/c" || c.KeyFile != "/x/k" || c.ClientCA != "/x/ca" {
		t.Fatalf("env not parsed: %+v", c)
	}
}

// TestMTLSEndToEnd 生成 CA + 服务端 + 客户端证书,以 RequireAndVerifyClientCert
// 启动 agent,并证明:(a) 持有有效证书的客户端可以访问 /healthz;
// (b) 不带证书的客户端会被拒绝。
func TestMTLSEndToEnd(t *testing.T) {
	dir := t.TempDir()
	caCert, caKey := genCA(t)
	writeCertPEM(t, filepath.Join(dir, "ca.crt"), caCert.Raw)

	serverCertDER, serverKey := genLeaf(t, caCert, caKey, "localhost", []string{"localhost"})
	writeCertPEM(t, filepath.Join(dir, "server.crt"), serverCertDER)
	writeKeyPEM(t, filepath.Join(dir, "server.key"), serverKey)

	tlsCfg, err := TLSConfig{
		Enabled:  true,
		CertFile: filepath.Join(dir, "server.crt"),
		KeyFile:  filepath.Join(dir, "server.key"),
		ClientCA: filepath.Join(dir, "ca.crt"),
	}.Build()
	if err != nil {
		t.Fatalf("Build mTLS: %v", err)
	}

	reg := tools.NewRegistry(datasource.NewMock())
	handler := New("prod-cn-1", reg, nil).Handler()

	ln, err := tls.Listen("tcp", "127.0.0.1:0", tlsCfg)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: handler}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()
	url := "https://" + ln.Addr().String() + "/healthz"

	// 信任我们自建 CA 的根证书池(两个客户端都用它校验服务端)。
	rootPool := x509.NewCertPool()
	rootPool.AddCert(caCert)

	// (a) 持有有效客户端证书 -> 成功。
	clientCertDER, clientKey := genLeaf(t, caCert, caKey, "control-plane", nil)
	clientTLSCert := tls.Certificate{
		Certificate: [][]byte{clientCertDER},
		PrivateKey:  clientKey,
	}
	okClient := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{
		RootCAs:      rootPool,
		Certificates: []tls.Certificate{clientTLSCert},
	}}}
	resp, err := okClient.Get(url)
	if err != nil {
		t.Fatalf("client with cert should succeed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d", resp.StatusCode)
	}
	resp.Body.Close()

	// (b) 不带客户端证书 -> 在 TLS 握手阶段被拒绝。
	noCertClient := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{
		RootCAs: rootPool,
	}}}
	if _, err := noCertClient.Get(url); err == nil {
		t.Fatal("client without cert must be rejected")
	}
}

// --- 证书辅助函数(仅测试用,自签名) ---

func genCA(t *testing.T) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "aiops-test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	return cert, key
}

func genLeaf(t *testing.T, ca *x509.Certificate, caKey *ecdsa.PrivateKey, cn string, dns []string) ([]byte, *ecdsa.PrivateKey) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		DNSNames:     dns,
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	return der, key
}

func writeCertPEM(t *testing.T, path string, der []byte) {
	t.Helper()
	b := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeKeyPEM(t *testing.T, path string, key *ecdsa.PrivateKey) {
	t.Helper()
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	b := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}
