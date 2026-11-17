package remote

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
	"os"
	"time"

	"google.golang.org/grpc/credentials"
)

const (
	EnvTLSCA   = "PACK_TARGET_TLS_CA"
	EnvTLSCert = "PACK_TARGET_TLS_CERT"
	EnvTLSKey  = "PACK_TARGET_TLS_KEY"
)

// TLSMaterial is the run-scoped CA and its client and server certificates, all PEM.
type TLSMaterial struct {
	CACert     []byte
	ClientCert []byte
	ClientKey  []byte
	ServerCert []byte
	ServerKey  []byte
}

func certSerial() (*big.Int, error) {
	return rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
}

func caCertificate() ([]byte, *ecdsa.PrivateKey, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	serial, err := certSerial()
	if err != nil {
		return nil, nil, err
	}
	now := time.Now().UTC()
	template := &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: "pack target run"},
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(2 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), key, nil
}

func leafCertificate(caDER []byte, caKey *ecdsa.PrivateKey, hosts []string, server bool) (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	serial, err := certSerial()
	if err != nil {
		return nil, nil, err
	}
	now := time.Now().UTC()
	certificate := &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: "pack target"},
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(2 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature,
	}
	if server {
		certificate.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
		for _, host := range hosts {
			if ip := net.ParseIP(host); ip != nil {
				certificate.IPAddresses = append(certificate.IPAddresses, ip)
			} else if host != "" {
				certificate.DNSNames = append(certificate.DNSNames, host)
			}
		}
	} else {
		certificate.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
	}
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		return nil, nil, err
	}
	der, err := x509.CreateCertificate(rand.Reader, certificate, ca, &key.PublicKey, caKey)
	if err != nil {
		return nil, nil, err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM, nil
}

// NewTLSMaterial makes a CA and certificates valid only for this run's network names.
func NewTLSMaterial(hosts []string) (*TLSMaterial, error) {
	ca, caKey, err := caCertificate()
	if err != nil {
		return nil, err
	}
	caDER, _ := pem.Decode(ca)
	if caDER == nil {
		return nil, fmt.Errorf("the generated CA is not PEM")
	}
	serverCert, serverKey, err := leafCertificate(caDER.Bytes, caKey, hosts, true)
	if err != nil {
		return nil, err
	}
	clientCert, clientKey, err := leafCertificate(caDER.Bytes, caKey, nil, false)
	if err != nil {
		return nil, err
	}
	return &TLSMaterial{CACert: ca, ClientCert: clientCert, ClientKey: clientKey, ServerCert: serverCert, ServerKey: serverKey}, nil
}

func (m *TLSMaterial) ServerCredentials() (credentials.TransportCredentials, error) {
	certificate, err := tls.X509KeyPair(m.ServerCert, m.ServerKey)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(m.CACert) {
		return nil, fmt.Errorf("the target's CA does not parse")
	}
	return credentials.NewTLS(&tls.Config{
		Certificates: []tls.Certificate{certificate}, ClientCAs: pool,
		ClientAuth: tls.RequireAndVerifyClientCert, MinVersion: tls.VersionTLS13,
	}), nil
}

func (m *TLSMaterial) ClientCredentials() (credentials.TransportCredentials, error) {
	certificate, err := tls.X509KeyPair(m.ClientCert, m.ClientKey)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(m.CACert) {
		return nil, fmt.Errorf("the target's CA does not parse")
	}
	return credentials.NewTLS(&tls.Config{
		Certificates: []tls.Certificate{certificate}, RootCAs: pool, MinVersion: tls.VersionTLS13,
	}), nil
}

func tlsMaterialFromEnv() (*TLSMaterial, bool, error) {
	ca, cert, key := os.Getenv(EnvTLSCA), os.Getenv(EnvTLSCert), os.Getenv(EnvTLSKey)
	if ca == "" && cert == "" && key == "" {
		return nil, false, nil
	}
	if ca == "" || cert == "" || key == "" {
		return nil, false, fmt.Errorf("%s, %s and %s must be set together", EnvTLSCA, EnvTLSCert, EnvTLSKey)
	}
	read := func(path string) ([]byte, error) { return os.ReadFile(path) }
	caPEM, err := read(ca)
	if err != nil {
		return nil, false, err
	}
	certPEM, err := read(cert)
	if err != nil {
		return nil, false, err
	}
	keyPEM, err := read(key)
	if err != nil {
		return nil, false, err
	}
	return &TLSMaterial{CACert: caPEM, ClientCert: certPEM, ClientKey: keyPEM, ServerCert: certPEM, ServerKey: keyPEM}, true, nil
}

func serverCredentialsFromEnv() (credentials.TransportCredentials, bool, error) {
	material, set, err := tlsMaterialFromEnv()
	if err != nil || !set {
		return nil, set, err
	}
	creds, err := material.ServerCredentials()
	return creds, true, err
}

func clientCredentialsFromEnv() (credentials.TransportCredentials, bool, error) {
	material, set, err := tlsMaterialFromEnv()
	if err != nil || !set {
		return nil, set, err
	}
	creds, err := material.ClientCredentials()
	return creds, true, err
}
