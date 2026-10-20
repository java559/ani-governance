package data

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"strings"
	"time"
)

func observabilityClientTLS(caFile, certFile, keyFile string) (*tls.Config, error) {
	if caFile == "" || certFile == "" || keyFile == "" {
		return nil, fmt.Errorf("observability CA, client certificate and key are required")
	}
	ca, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("read observability CA: %w", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		return nil, fmt.Errorf("observability CA contains no certificates")
	}
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("load governance certificate: %w", err)
	}
	if len(cert.Certificate) == 0 {
		return nil, fmt.Errorf("governance certificate is empty")
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return nil, fmt.Errorf("parse governance certificate: %w", err)
	}
	if len(leaf.DNSNames) != 1 || leaf.DNSNames[0] != "ani-governance" || len(leaf.URIs) != 0 || len(leaf.IPAddresses) != 0 || len(leaf.EmailAddresses) != 0 {
		return nil, fmt.Errorf("governance certificate requires only DNS SAN ani-governance")
	}
	now := time.Now()
	if now.Before(leaf.NotBefore) || !now.Before(leaf.NotAfter) {
		return nil, fmt.Errorf("governance certificate is not valid now")
	}
	usageOK := len(leaf.ExtKeyUsage) == 0
	for _, usage := range leaf.ExtKeyUsage {
		usageOK = usageOK || usage == x509.ExtKeyUsageClientAuth || usage == x509.ExtKeyUsageAny
	}
	if !usageOK {
		return nil, fmt.Errorf("governance certificate does not permit client authentication")
	}
	return &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots, Certificates: []tls.Certificate{cert}, ServerName: "ani-observability-service", VerifyConnection: func(state tls.ConnectionState) error {
		if len(state.VerifiedChains) == 0 || len(state.PeerCertificates) == 0 {
			return fmt.Errorf("unverified observability server")
		}
		found := false
		for _, dns := range state.PeerCertificates[0].DNSNames {
			if strings.Contains(dns, "*") {
				return fmt.Errorf("wildcard observability identity forbidden")
			}
			found = found || dns == "ani-observability-service"
		}
		if !found {
			return fmt.Errorf("observability exact DNS SAN mismatch")
		}
		return nil
	}}, nil
}
