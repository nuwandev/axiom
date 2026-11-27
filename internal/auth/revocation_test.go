package auth

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
	"strings"
	"testing"
	"time"
)

type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
}

func newCA(t *testing.T, cn string) *testCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return &testCA{cert: cert, key: key}
}

func (ca *testCA) leaf(t *testing.T, serial int64) *x509.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(serial),
		Subject:      pkix.Name{CommonName: "client"},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}

// crlFile writes a CRL signed by ca and returns its path.
func (ca *testCA) crlFile(t *testing.T, asPEM bool, nextUpdate time.Time, revoked ...int64) string {
	t.Helper()
	var entries []x509.RevocationListEntry
	for _, s := range revoked {
		entries = append(entries, x509.RevocationListEntry{SerialNumber: big.NewInt(s), RevocationTime: time.Now().Add(-time.Minute)})
	}
	der, err := x509.CreateRevocationList(rand.Reader, &x509.RevocationList{
		Number:                    big.NewInt(1),
		ThisUpdate:                time.Now().Add(-time.Hour),
		NextUpdate:                nextUpdate,
		RevokedCertificateEntries: entries,
	}, ca.cert, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	data := der
	if asPEM {
		data = pem.EncodeToMemory(&pem.Block{Type: "X509 CRL", Bytes: der})
	}
	p := filepath.Join(t.TempDir(), "list.crl")
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRevocationList_RevokedSerialIsRejectedOthersPass(t *testing.T) {
	ca := newCA(t, "ca")
	for _, asPEM := range []bool{true, false} {
		list, err := loadRevocationList(ca.crlFile(t, asPEM, time.Now().Add(time.Hour), 7), []*x509.Certificate{ca.cert}, time.Now())
		if err != nil {
			t.Fatalf("pem=%v: loadRevocationList: %v", asPEM, err)
		}
		if err := list.check(ca.leaf(t, 7), time.Now()); err == nil || !strings.Contains(err.Error(), "revoked") {
			t.Fatalf("pem=%v: serial 7 error = %v, want revoked", asPEM, err)
		}
		if err := list.check(ca.leaf(t, 8), time.Now()); err != nil {
			t.Fatalf("pem=%v: serial 8 rejected: %v", asPEM, err)
		}
	}
}

func TestRevocationList_SameSerialFromAnotherIssuerIsNotRevoked(t *testing.T) {
	caA, caB := newCA(t, "ca-a"), newCA(t, "ca-b")
	list, err := loadRevocationList(caA.crlFile(t, true, time.Now().Add(time.Hour), 7), []*x509.Certificate{caA.cert, caB.cert}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := list.check(caB.leaf(t, 7), time.Now()); err != nil {
		t.Fatalf("a serial revoked by CA A must not revoke CA B's certificate: %v", err)
	}
}

func TestRevocationList_NilListChecksNothing(t *testing.T) {
	ca := newCA(t, "ca")
	var list *revocationList
	if err := list.check(ca.leaf(t, 1), time.Now()); err != nil {
		t.Fatalf("nil list rejected a certificate: %v", err)
	}
}

func TestRevocationList_ForeignSignatureIsRefused(t *testing.T) {
	trusted, other := newCA(t, "trusted"), newCA(t, "other")
	_, err := loadRevocationList(other.crlFile(t, true, time.Now().Add(time.Hour), 7), []*x509.Certificate{trusted.cert}, time.Now())
	if err == nil || !strings.Contains(err.Error(), "not signed by any CA") {
		t.Fatalf("error = %v, want one about the signature", err)
	}
}

func TestRevocationList_ExpiredAtLoadIsRefused(t *testing.T) {
	ca := newCA(t, "ca")
	_, err := loadRevocationList(ca.crlFile(t, true, time.Now().Add(-time.Minute)), []*x509.Certificate{ca.cert}, time.Now())
	if err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("error = %v, want expired", err)
	}
}

// A list that was current when loaded goes stale while the agent runs; from
// NextUpdate on every client is refused until a fresh list is reloaded.
func TestRevocationList_GoesStaleWhileRunning(t *testing.T) {
	ca := newCA(t, "ca")
	next := time.Now().Add(time.Hour)
	list, err := loadRevocationList(ca.crlFile(t, true, next), []*x509.Certificate{ca.cert}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	leaf := ca.leaf(t, 5)
	if err := list.check(leaf, next.Add(-time.Second)); err != nil {
		t.Fatalf("before NextUpdate: %v", err)
	}
	if err := list.check(leaf, next); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("at NextUpdate: error = %v, want expired", err)
	}
}

func TestRevocationList_GarbageFileIsRefused(t *testing.T) {
	ca := newCA(t, "ca")
	p := filepath.Join(t.TempDir(), "bad.crl")
	if err := os.WriteFile(p, []byte("not a crl"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadRevocationList(p, []*x509.Certificate{ca.cert}, time.Now()); err == nil {
		t.Fatal("garbage CRL file accepted")
	}
}
