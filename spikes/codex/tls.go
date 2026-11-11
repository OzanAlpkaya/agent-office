package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"flag"
	"log"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

// makeTLS writes a throwaway CA (ca.pem) and a loopback server certificate
// signed by it (server.pem, server-priv.pem) into -dir. The CA's private part
// is not kept: nothing else can be signed with it.
func makeTLS(args []string) {
	fs := flag.NewFlagSet("tls", flag.ExitOnError)
	dir := fs.String("dir", "out/tls", "")
	_ = fs.Parse(args)
	if err := os.MkdirAll(*dir, 0o700); err != nil {
		log.Fatal(err)
	}
	caPriv, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	now := time.Now()
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "agent-office spike CA"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(30 * 24 * time.Hour), IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign, MaxPathLenZero: true}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &caPriv.PublicKey, caPriv)
	if err != nil {
		log.Fatal(err)
	}
	caCert, _ := x509.ParseCertificate(caDER)
	srvPriv, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	srv := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "127.0.0.1"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(30 * 24 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		DNSNames: []string{"localhost"}}
	srvDER, err := x509.CreateCertificate(rand.Reader, srv, caCert, &srvPriv.PublicKey, caPriv)
	if err != nil {
		log.Fatal(err)
	}
	privDER, _ := x509.MarshalECPrivateKey(srvPriv)
	write := func(name, typ string, der []byte, mode os.FileMode) {
		if err := os.WriteFile(filepath.Join(*dir, name), pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der}), mode); err != nil {
			log.Fatal(err)
		}
	}
	write("ca.pem", "CERTIFICATE", caDER, 0o644)
	write("server.pem", "CERTIFICATE", srvDER, 0o644)
	write("server-priv.pem", "EC PRIVATE KEY", privDER, 0o600)
}
