package server

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func issueQuotaCert(t *testing.T, ca *x509.Certificate, caKey *ecdsa.PrivateKey, names []string, usage x509.ExtKeyUsage, dir, file string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), DNSNames: names, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{usage}}
	der, err := x509.CreateCertificate(rand.Reader, template, ca, &key.PublicKey, caKey)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, file+".pem"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600))
	raw, err := x509.MarshalECPrivateKey(key)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, file+".key"), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: raw}), 0600))
}

func quotaTLSFixture(t *testing.T) (string, *x509.CertPool) {
	t.Helper()
	dir := t.TempDir()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	caTemplate := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "quota-test-ca"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	der, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &key.PublicKey, key)
	require.NoError(t, err)
	ca, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ca.pem"), caPEM, 0600))
	pool := x509.NewCertPool()
	require.True(t, pool.AppendCertsFromPEM(caPEM))
	issueQuotaCert(t, ca, key, []string{"ani-governance"}, x509.ExtKeyUsageServerAuth, dir, "server")
	issueQuotaCert(t, ca, key, []string{"ani-inference"}, x509.ExtKeyUsageClientAuth, dir, "owner")
	issueQuotaCert(t, ca, key, []string{"ani-resource-service"}, x509.ExtKeyUsageClientAuth, dir, "resource")
	issueQuotaCert(t, ca, key, []string{"other-service"}, x509.ExtKeyUsageClientAuth, dir, "unknown")
	issueQuotaCert(t, ca, key, []string{"ani-inference", "other-owner"}, x509.ExtKeyUsageClientAuth, dir, "mixed")
	issueQuotaCert(t, ca, key, []string{"ani-inference", "ani-inference-alias"}, x509.ExtKeyUsageClientAuth, dir, "alias")
	return dir, pool
}
