package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func renewCertificate(out, hosts string) error {
	read := func(name string) ([]byte, error) {
		b, e := os.ReadFile(filepath.Join(out, name))
		if e != nil {
			return nil, e
		}
		p, _ := pem.Decode(b)
		if p == nil {
			return nil, fmt.Errorf("PEM inválido %s", name)
		}
		return p.Bytes, nil
	}
	b, e := read("ca.crt")
	if e != nil {
		return e
	}
	ca, e := x509.ParseCertificate(b)
	if e != nil {
		return e
	}
	b, e = read("ca.key")
	if e != nil {
		return e
	}
	caKey, e := x509.ParsePKCS1PrivateKey(b)
	if e != nil {
		return e
	}
	key, e := rsa.GenerateKey(rand.Reader, 3072)
	if e != nil {
		return e
	}
	serial, e := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if e != nil {
		return e
	}
	der, e := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if e != nil {
		return e
	}
	ski := sha256.Sum256(der)
	cert := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "DFSha cluster"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().AddDate(1, 0, 0), AuthorityKeyId: ca.SubjectKeyId, SubjectKeyId: ski[:20], KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}}
	for _, h := range strings.Split(hosts, ",") {
		h = strings.TrimSpace(h)
		if ip := net.ParseIP(h); ip != nil {
			cert.IPAddresses = append(cert.IPAddresses, ip)
		} else if h != "" {
			cert.DNSNames = append(cert.DNSNames, h)
		}
	}
	der, e = x509.CreateCertificate(rand.Reader, cert, ca, &key.PublicKey, caKey)
	if e != nil {
		return e
	}
	if e = os.WriteFile(filepath.Join(out, "cluster.crt"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); e != nil {
		return e
	}
	return os.WriteFile(filepath.Join(out, "cluster.key"), pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), 0600)
}
