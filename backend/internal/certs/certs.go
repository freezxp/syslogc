// Package certs reads and describes TLS certificate material.
//
// A certificate can be given as a path to a file or as the PEM text itself.
// Paths suit a host where something else — cert-manager, certbot, a
// configuration manager — writes and renews the files. Pasted PEM suits
// everyone else: a container filesystem is usually read-only, so there is
// nowhere for a person working in the web interface to put a file.
//
// Describing the material matters as much as loading it. Someone pasting a
// certificate wants to see what they pasted — which names it covers and when
// it expires — because the alternative is discovering a mismatch when a
// device fails to connect, or an expiry when it stops.
package certs

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// MaxPEMBytes bounds one piece of pasted material. A certificate chain is a
// few kilobytes; far more than this is a mistake or an attempt at one.
const MaxPEMBytes = 1 << 20

// Material is a certificate and its key, from files or from PEM text.
type Material struct {
	// CertFile and KeyFile are paths read at load time and watched for
	// renewal. Ignored when Cert and Key are set.
	CertFile, KeyFile string
	// Cert and Key are PEM text. They take precedence over the paths.
	Cert, Key string
}

// Inline reports whether the material is PEM text rather than files.
func (m Material) Inline() bool { return strings.TrimSpace(m.Cert) != "" }

// Empty reports whether no certificate was given at all.
func (m Material) Empty() bool { return !m.Inline() && m.CertFile == "" && m.KeyFile == "" }

// Info describes a certificate in the terms someone setting one up asks
// about: whether it is the right certificate, and how long it lasts.
type Info struct {
	Subject string `json:"subject"`
	Issuer  string `json:"issuer"`
	// DNSNames and IPAddresses are what the certificate is valid for. A
	// sender whose address is not among them will reject the connection.
	DNSNames    []string  `json:"dns_names,omitempty"`
	IPAddresses []string  `json:"ip_addresses,omitempty"`
	NotBefore   time.Time `json:"not_before"`
	NotAfter    time.Time `json:"not_after"`
	// SelfSigned certificates work, but every sender must be told to trust
	// this one specifically.
	SelfSigned bool `json:"self_signed"`
	// Chain is how many certificates were given. One is common and fine for
	// a self-signed certificate; a certificate from a public authority
	// usually needs its intermediates too, or senders cannot build a path.
	Chain int `json:"chain"`
}

// Expired reports whether the certificate is outside its validity at t.
func (i Info) Expired(t time.Time) bool { return t.After(i.NotAfter) || t.Before(i.NotBefore) }

// ExpiresWithin reports whether the certificate runs out within d of t.
func (i Info) ExpiresWithin(t time.Time, d time.Duration) bool { return i.NotAfter.Sub(t) < d }

// Load turns material into a certificate, reporting what it found.
//
// The error says which part is wrong in the terms the person pasting it can
// act on: a key that belongs to another certificate, a certificate where a
// key should be, text that is not PEM at all.
func Load(m Material) (*tls.Certificate, Info, error) {
	var certPEM, keyPEM []byte
	if m.Inline() {
		certPEM, keyPEM = []byte(m.Cert), []byte(m.Key)
		if strings.TrimSpace(m.Key) == "" {
			return nil, Info{}, errors.New("a certificate was given without its private key")
		}
	} else {
		if m.CertFile == "" || m.KeyFile == "" {
			return nil, Info{}, errors.New("both a certificate and a private key are required")
		}
		var err error
		if certPEM, err = readFile(m.CertFile, "certificate"); err != nil {
			return nil, Info{}, err
		}
		if keyPEM, err = readFile(m.KeyFile, "private key"); err != nil {
			return nil, Info{}, err
		}
	}

	info, err := describe(certPEM)
	if err != nil {
		return nil, Info{}, err
	}
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		// The pairing error from crypto/tls is accurate but opaque; the two
		// cases worth naming are the ones people actually hit.
		msg := err.Error()
		switch {
		case strings.Contains(msg, "private key does not match"):
			return nil, Info{}, errors.New("the private key does not belong to this certificate")
		case strings.Contains(msg, "found a certificate rather than a key"):
			return nil, Info{}, errors.New("the private key field holds a certificate; the key is the other file, " +
				"beginning with -----BEGIN PRIVATE KEY----- or -----BEGIN RSA PRIVATE KEY-----")
		case strings.Contains(msg, "failed to find any PEM data") || strings.Contains(msg, "failed to parse private key"):
			return nil, Info{}, errors.New("the private key is not readable PEM; it should begin with -----BEGIN PRIVATE KEY----- or similar")
		}
		return nil, Info{}, fmt.Errorf("certificate and key: %w", err)
	}
	return &cert, info, nil
}

// Describe reports what a certificate covers, without needing its key.
func Describe(certPEM string) (Info, error) { return describe([]byte(certPEM)) }

func describe(certPEM []byte) (Info, error) {
	if len(certPEM) > MaxPEMBytes {
		return Info{}, fmt.Errorf("the certificate is larger than %d bytes", MaxPEMBytes)
	}
	var leaf *x509.Certificate
	chain := 0
	rest := certPEM
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		chain++
		if leaf == nil {
			c, err := x509.ParseCertificate(block.Bytes)
			if err != nil {
				return Info{}, fmt.Errorf("the certificate could not be read: %w", err)
			}
			leaf = c
		}
	}
	if leaf == nil {
		if containsKey(certPEM) {
			return Info{}, errors.New("that is a private key, not a certificate; the certificate begins with -----BEGIN CERTIFICATE-----")
		}
		return Info{}, errors.New("no certificate found; it should begin with -----BEGIN CERTIFICATE-----")
	}
	info := Info{
		Subject:    leaf.Subject.CommonName,
		Issuer:     leaf.Issuer.CommonName,
		DNSNames:   leaf.DNSNames,
		NotBefore:  leaf.NotBefore.UTC(),
		NotAfter:   leaf.NotAfter.UTC(),
		SelfSigned: leaf.Issuer.String() == leaf.Subject.String(),
		Chain:      chain,
	}
	if info.Subject == "" {
		info.Subject = leaf.Subject.String()
	}
	if info.Issuer == "" {
		info.Issuer = leaf.Issuer.String()
	}
	for _, ip := range leaf.IPAddresses {
		info.IPAddresses = append(info.IPAddresses, ip.String())
	}
	return info, nil
}

func containsKey(pemBytes []byte) bool {
	return strings.Contains(string(pemBytes), "PRIVATE KEY")
}

// ClientCAs builds a pool from PEM text or a file.
func ClientCAs(inline, file string) (*x509.CertPool, error) {
	data := []byte(inline)
	if strings.TrimSpace(inline) == "" {
		if file == "" {
			return nil, nil
		}
		var err error
		if data, err = readFile(file, "client CA"); err != nil {
			return nil, err
		}
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(data) {
		return nil, errors.New("the client CA holds no certificates")
	}
	return pool, nil
}

func readFile(path, what string) ([]byte, error) {
	data, err := os.ReadFile(path) //nolint:gosec // operator-configured path
	if err != nil {
		if os.IsNotExist(err) {
			// The common case by far, and the message people see when they
			// typed a path into a container that cannot hold one.
			return nil, fmt.Errorf("no %s at %s. A file has to exist inside the server's filesystem; "+
				"paste the PEM instead if you cannot put one there", what, path)
		}
		return nil, fmt.Errorf("reading the %s: %w", what, err)
	}
	if len(data) > MaxPEMBytes {
		return nil, fmt.Errorf("the %s is larger than %d bytes", what, MaxPEMBytes)
	}
	return data, nil
}
