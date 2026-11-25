package auth

import (
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"time"
)

// revocationList is a parsed, signature-checked CRL reduced to what the
// per-request check needs.
type revocationList struct {
	// revoked is keyed by issuer and serial, because serial numbers are only
	// unique per issuer and the CA file may hold more than one CA.
	revoked    map[string]struct{}
	nextUpdate time.Time
}

func revocationKey(rawIssuer []byte, serial fmt.Stringer) string {
	return string(rawIssuer) + "|" + serial.String()
}

// loadRevocationList reads a PEM or DER CRL and requires that one of cas
// signed it: an unsigned or foreign list would let anyone who can write the
// file revoke (or fail to revoke) at will. A list that is already past its
// NextUpdate is refused at load time, since it can no longer be trusted to be
// current.
func loadRevocationList(path string, cas []*x509.Certificate, now time.Time) (*revocationList, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading CRL file: %w", err)
	}
	if block, _ := pem.Decode(data); block != nil {
		if block.Type != "X509 CRL" {
			return nil, fmt.Errorf("CRL file %q holds a %q PEM block, expected X509 CRL", path, block.Type)
		}
		data = block.Bytes
	}
	crl, err := x509.ParseRevocationList(data)
	if err != nil {
		return nil, fmt.Errorf("parsing CRL file %q: %w", path, err)
	}

	signed := false
	for _, ca := range cas {
		if string(ca.RawSubject) == string(crl.RawIssuer) && crl.CheckSignatureFrom(ca) == nil {
			signed = true
			break
		}
	}
	if !signed {
		return nil, fmt.Errorf("CRL file %q is not signed by any CA in the CA file", path)
	}
	if !crl.NextUpdate.IsZero() && !now.Before(crl.NextUpdate) {
		return nil, fmt.Errorf("CRL file %q expired at %s; publish a fresh list and reload", path, crl.NextUpdate.UTC().Format(time.RFC3339))
	}

	list := &revocationList{revoked: make(map[string]struct{}, len(crl.RevokedCertificateEntries)), nextUpdate: crl.NextUpdate}
	for _, e := range crl.RevokedCertificateEntries {
		list.revoked[revocationKey(crl.RawIssuer, e.SerialNumber)] = struct{}{}
	}
	return list, nil
}

// check rejects a revoked certificate, and every certificate once the list has
// gone stale: after NextUpdate the list may be missing revocations, so the
// agent fails closed instead of trusting it.
func (l *revocationList) check(leaf *x509.Certificate, now time.Time) error {
	if l == nil {
		return nil
	}
	if !l.nextUpdate.IsZero() && !now.Before(l.nextUpdate) {
		return fmt.Errorf("certificate revocation list expired at %s", l.nextUpdate.UTC().Format(time.RFC3339))
	}
	if _, revoked := l.revoked[revocationKey(leaf.RawIssuer, leaf.SerialNumber)]; revoked {
		return fmt.Errorf("client certificate serial %s has been revoked", leaf.SerialNumber)
	}
	return nil
}

// parseCertificates decodes every CERTIFICATE block, so the CA certificates
// are kept for CRL signature checks as well as added to the trust pool.
func parseCertificates(pemData []byte) []*x509.Certificate {
	var certs []*x509.Certificate
	for {
		var block *pem.Block
		block, pemData = pem.Decode(pemData)
		if block == nil {
			return certs
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		if cert, err := x509.ParseCertificate(block.Bytes); err == nil {
			certs = append(certs, cert)
		}
	}
}
