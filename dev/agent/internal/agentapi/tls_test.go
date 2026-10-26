package agentapi

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestMTLSRejectsMissingClientAndUntrustedServer(t *testing.T) {
	caPublic, caPrivate, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "G503 test CA"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, caPublic, caPrivate)
	require.NoError(t, err)
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	roots := x509.NewCertPool()
	require.True(t, roots.AppendCertsFromPEM(caPEM))
	issue := func(serial int64, usage x509.ExtKeyUsage) ([]byte, []byte) {
		public, private, err := ed25519.GenerateKey(rand.Reader)
		require.NoError(t, err)
		leaf := &x509.Certificate{SerialNumber: big.NewInt(serial), NotBefore: ca.NotBefore, NotAfter: ca.NotAfter, DNSNames: []string{"localhost"}, ExtKeyUsage: []x509.ExtKeyUsage{usage}, KeyUsage: x509.KeyUsageDigitalSignature}
		der, err := x509.CreateCertificate(rand.Reader, leaf, ca, public, caPrivate)
		require.NoError(t, err)
		key, err := x509.MarshalPKCS8PrivateKey(private)
		require.NoError(t, err)
		return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key})
	}
	serverCert, serverKey := issue(2, x509.ExtKeyUsageServerAuth)
	serverPair, err := tls.X509KeyPair(serverCert, serverKey)
	require.NoError(t, err)
	clientCert, clientKey := issue(3, x509.ExtKeyUsageClientAuth)
	dir := t.TempDir()
	paths := TLSConfig{CertFile: filepath.Join(dir, "client.crt"), KeyFile: filepath.Join(dir, "client.key"), CAFile: filepath.Join(dir, "ca.crt")}
	for path, data := range map[string][]byte{paths.CertFile: clientCert, paths.KeyFile: clientKey, paths.CAFile: caPEM} {
		require.NoError(t, os.WriteFile(path, data, 0o600))
	}
	for _, scenario := range []string{"valid", "missing-client", "untrusted-server"} {
		t.Run(scenario, func(t *testing.T) {
			clientConfig, err := loadTLSConfig(paths)
			require.NoError(t, err)
			clientConfig.ServerName = "localhost"
			// TLS 1.2 makes missing-client rejection part of the handshake on both ends.
			clientConfig.MaxVersion = tls.VersionTLS12
			if scenario == "missing-client" {
				clientConfig.Certificates = nil
			}
			if scenario == "untrusted-server" {
				clientConfig.RootCAs = x509.NewCertPool()
			}
			left, right := net.Pipe()
			defer left.Close()
			defer right.Close()
			require.NoError(t, left.SetDeadline(time.Now().Add(3*time.Second)))
			require.NoError(t, right.SetDeadline(time.Now().Add(3*time.Second)))
			server := tls.Server(left, &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{serverPair}, ClientCAs: roots, ClientAuth: tls.RequireAndVerifyClientCert})
			done := make(chan error, 1)
			go func() { done <- server.Handshake() }()
			clientErr := tls.Client(right, clientConfig).Handshake()
			serverErr := <-done
			if scenario == "valid" {
				require.NoError(t, clientErr)
				require.NoError(t, serverErr)
			} else {
				require.Error(t, clientErr)
				require.Error(t, serverErr)
			}
		})
	}
	_, err = loadTLSConfig(TLSConfig{CAFile: paths.CAFile})
	require.Error(t, err)
}
