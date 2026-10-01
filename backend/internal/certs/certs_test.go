package certs

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// generate makes a certificate and key the way a person setting up a source
// would have been given them.
func generate(t *testing.T, cn string, dns []string, ips []string, notAfter time.Time) (certPEM, keyPEM string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     notAfter,
		DNSNames:     dns,
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		IsCA:         true,
	}
	for _, ip := range ips {
		tmpl.IPAddresses = append(tmpl.IPAddresses, net.ParseIP(ip))
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}))
}

func TestLoadsPastedMaterialAndDescribesIt(t *testing.T) {
	certPEM, keyPEM := generate(t, "syslog.example.com",
		[]string{"syslog.example.com", "logs.example.com"}, []string{"10.0.0.5"},
		time.Now().Add(90*24*time.Hour))

	cert, info, err := Load(Material{Cert: certPEM, Key: keyPEM})
	if err != nil {
		t.Fatal(err)
	}
	if cert == nil || len(cert.Certificate) == 0 {
		t.Fatal("no certificate was loaded")
	}
	// What someone pasting a certificate wants confirmed: that it is the
	// right one, and how long it lasts.
	if info.Subject != "syslog.example.com" {
		t.Errorf("subject = %q", info.Subject)
	}
	if len(info.DNSNames) != 2 || info.DNSNames[0] != "syslog.example.com" {
		t.Errorf("dns names = %v", info.DNSNames)
	}
	if len(info.IPAddresses) != 1 || info.IPAddresses[0] != "10.0.0.5" {
		t.Errorf("ip addresses = %v", info.IPAddresses)
	}
	if !info.SelfSigned {
		t.Error("a self-signed certificate was not reported as one")
	}
	if info.Chain != 1 {
		t.Errorf("chain = %d", info.Chain)
	}
	if info.Expired(time.Now()) {
		t.Error("a certificate valid for 90 days was reported expired")
	}
	if info.ExpiresWithin(time.Now(), 30*24*time.Hour) {
		t.Error("a certificate valid for 90 days was reported as expiring within 30")
	}
	if !info.ExpiresWithin(time.Now(), 180*24*time.Hour) {
		t.Error("a certificate valid for 90 days was not reported as expiring within 180")
	}
}

func TestMistakesAreNamedInTermsYouCanActOn(t *testing.T) {
	certPEM, keyPEM := generate(t, "a.example.com", nil, nil, time.Now().Add(time.Hour))
	_, otherKey := generate(t, "b.example.com", nil, nil, time.Now().Add(time.Hour))

	for _, tc := range []struct {
		name string
		m    Material
		want string
	}{
		{"key of another certificate", Material{Cert: certPEM, Key: otherKey}, "does not belong to this certificate"},
		{"certificate pasted as the key", Material{Cert: certPEM, Key: certPEM}, "holds a certificate; the key is the other file"},
		{"key pasted as the certificate", Material{Cert: keyPEM, Key: keyPEM}, "that is a private key, not a certificate"},
		{"nothing but prose", Material{Cert: "paste your certificate here", Key: keyPEM}, "no certificate found"},
		{"certificate without a key", Material{Cert: certPEM}, "without its private key"},
		{"neither", Material{}, "both a certificate and a private key"},
	} {
		_, _, err := Load(tc.m)
		if err == nil {
			t.Errorf("%s: accepted", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error = %q, want it to mention %q", tc.name, err, tc.want)
		}
	}
}

func TestAMissingFileSaysWhereToPutOneOrPasteInstead(t *testing.T) {
	_, _, err := Load(Material{CertFile: "/etc/syslogc/tls/server.crt", KeyFile: "/etc/syslogc/tls/server.key"})
	if err == nil {
		t.Fatal("a missing file was accepted")
	}
	// The message people see when they typed a path into a container that
	// has nowhere to hold one; it has to point at the way out.
	for _, want := range []string{"/etc/syslogc/tls/server.crt", "paste the PEM instead"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to mention %q", err, want)
		}
	}
}

func TestFilesStillWork(t *testing.T) {
	certPEM, keyPEM := generate(t, "file.example.com", nil, nil, time.Now().Add(time.Hour))
	dir := t.TempDir()
	certFile := filepath.Join(dir, "server.crt")
	keyFile := filepath.Join(dir, "server.key")
	if err := os.WriteFile(certFile, []byte(certPEM), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, []byte(keyPEM), 0o600); err != nil {
		t.Fatal(err)
	}
	_, info, err := Load(Material{CertFile: certFile, KeyFile: keyFile})
	if err != nil {
		t.Fatal(err)
	}
	if info.Subject != "file.example.com" {
		t.Errorf("subject = %q", info.Subject)
	}

	// Pasted material wins, so a source that gains a pasted certificate
	// stops reading whatever the paths used to hold.
	pastedCert, pastedKey := generate(t, "pasted.example.com", nil, nil, time.Now().Add(time.Hour))
	_, info, err = Load(Material{CertFile: certFile, KeyFile: keyFile, Cert: pastedCert, Key: pastedKey})
	if err != nil {
		t.Fatal(err)
	}
	if info.Subject != "pasted.example.com" {
		t.Errorf("subject = %q, want the pasted certificate to take precedence", info.Subject)
	}
}

func TestExpiryIsReported(t *testing.T) {
	expired, expiredKey := generate(t, "old.example.com", nil, nil, time.Now().Add(-time.Hour))
	_, info, err := Load(Material{Cert: expired, Key: expiredKey})
	// An expired certificate still loads: refusing it would take a source
	// down at the moment its certificate lapses, which is worse than
	// serving it and saying so.
	if err != nil {
		t.Fatalf("an expired certificate was refused: %v", err)
	}
	if !info.Expired(time.Now()) {
		t.Error("an expired certificate was not reported as expired")
	}
}

func TestClientCAs(t *testing.T) {
	caPEM, _ := generate(t, "ca.example.com", nil, nil, time.Now().Add(time.Hour))
	pool, err := ClientCAs(caPEM, "")
	if err != nil || pool == nil {
		t.Fatalf("pool = %v, err = %v", pool, err)
	}
	if _, err := ClientCAs("not a certificate", ""); err == nil {
		t.Error("text that is not a certificate was accepted as a CA")
	}
	// No CA at all is not an error: client certificates are optional.
	if pool, err := ClientCAs("", ""); err != nil || pool != nil {
		t.Errorf("pool = %v, err = %v, want neither", pool, err)
	}
}
