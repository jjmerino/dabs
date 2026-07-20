//go:build e2e

// Loopback fakes that stand in for the real internet, so the egress suite proves
// its properties hermetically. A policy that ALLOWS a host, or a window that
// PASSES a host through un-decrypted, is only meaningful if the tunnel actually
// reaches an upstream and carries its bytes back. These helpers are that
// upstream: a TLS (or plain-HTTP) server on the suite process's own 127.0.0.1.
//
// The engine raw-tunnels an allowed/passed-through CONNECT with
// net.connect(port, host) on the plaintext CONNECT host, and it runs in the same
// process space as this suite, so a box that CONNECTs to 127.0.0.1:<port> lands
// on the server started here. The box's proxy env exempts 127.0.0.1 via NO_PROXY,
// so the in-box curl passes an empty --noproxy value to clear that exemption and
// route the request through the engine (which then dials this loopback listener).
package e2e

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"testing"
	"time"
)

// mintSelfSignedLoopback mints a self-signed leaf for 127.0.0.1 (with the IP SAN
// a client needs to validate a literal-IP host) and returns the TLS certificate
// plus its PEM (which, being self-signed, is also the CA a client trusts).
func mintSelfSignedLoopback(t *testing.T) (tls.Certificate, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "127.0.0.1"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:           []net.IP{net.IPv4(127, 0, 0, 1)},
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	return cert, certPEM
}

// startLoopbackTLS runs a TLS server on 127.0.0.1 that answers every request with
// body, writes its trust anchor PEM to caPath (mounted into the box so its curl
// can --cacert it), and returns the port. It is torn down at test end.
func startLoopbackTLS(t *testing.T, caPath, body string) int {
	t.Helper()
	cert, caPEM := mintSelfSignedLoopback(t)
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}})
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, body)
	})}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	if err := os.WriteFile(caPath, caPEM, 0o644); err != nil {
		t.Fatal(err)
	}
	return ln.Addr().(*net.TCPAddr).Port
}

// startLoopbackHTTP runs a plain-HTTP server on 127.0.0.1 answering with body,
// and returns the port. Used as a positive control the suite process itself can
// reach — proving the server is up — against which a cut-network box then fails.
func startLoopbackHTTP(t *testing.T, body string) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, body)
	})}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return ln.Addr().(*net.TCPAddr).Port
}
