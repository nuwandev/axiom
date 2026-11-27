package auth

import (
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"time"
)

// revocationList is one or more parsed, signature-checked CRLs reduced to
// what the per-request check needs.
type revocationList struct {
	// revoked is keyed by issuer and serial, because serial numbers are only
	// unique per issuer and the CA file may hold more than one CA.
	revoked map[string]struct{}
	// nextUpdate is the earliest NextUpdate of all the lists; zero if none
	// of them has one. Once it passes, at least one list may be missing
	// revocations.
	nextUpdate time.Time
}

func revocationKey(rawIssuer []byte, serial fmt.Stringer) string {
	return string(rawIssuer) + "|" + serial.String()
}

// loadRevocationList reads a file holding one DER CRL or any number of PEM
// "X509 CRL" blocks, one per issuing CA. Each list must be signed by a CA in
// cas: an unsigned or foreign list would let anyone who can write the file
// revoke (or fail to revoke) at will. Two lists from the same issuer are
// refused, since which one is current would be ambiguous. A list that is
// already past its NextUpdate is refused at load time, since it can no longer
// be trusted to be current.
func loadRevocationList(path string, cas []*x509.Certificate, now time.Time) (*revocationList, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading CRL file: %w", err)
	}
	ders, err := crlBlocks(path, data)
	if err != nil {
		return nil, err
	}

	list := &revocationList{revoked: make(map[string]struct{})}
	issuers := make(map[string]struct{}, len(ders))
	for i, der := range ders {
		crl, err := x509.ParseRevocationList(der)
		if err != nil {
			return nil, fmt.Errorf("parsing CRL %d in %q: %w", i+1, path, err)
		}
		if _, dup := issuers[string(crl.RawIssuer)]; dup {
			return nil, fmt.Errorf("CRL file %q holds more than one list from the same issuer", path)
		}
		issuers[string(crl.RawIssuer)] = struct{}{}

		if !signedByOneOf(crl, cas) {
			return nil, fmt.Errorf("CRL %d in %q is not signed by any CA in the CA file", i+1, path)
		}
		if !crl.NextUpdate.IsZero() {
			if !now.Before(crl.NextUpdate) {
				return nil, fmt.Errorf("CRL %d in %q expired at %s; publish a fresh list and reload", i+1, path, crl.NextUpdate.UTC().Format(time.RFC3339))
			}
			if list.nextUpdate.IsZero() || crl.NextUpdate.Before(list.nextUpdate) {
				list.nextUpdate = crl.NextUpdate
			}
		}
		for _, e := range crl.RevokedCertificateEntries {
			list.revoked[revocationKey(crl.RawIssuer, e.SerialNumber)] = struct{}{}
		}
	}
	return list, nil
}

// crlBlocks returns the DER of every CRL in data. A file with no PEM block is
// taken as a single DER list; a PEM file must hold only X509 CRL blocks, so a
// certificate pasted in by mistake is an error rather than silently skipped.
func crlBlocks(path string, data []byte) ([][]byte, error) {
	block, rest := pem.Decode(data)
	if block == nil {
		return [][]byte{data}, nil
	}
	var ders [][]byte
	for block != nil {
		if block.Type != "X509 CRL" {
			return nil, fmt.Errorf("CRL file %q holds a %q PEM block, expected X509 CRL", path, block.Type)
		}
		ders = append(ders, block.Bytes)
		block, rest = pem.Decode(rest)
	}
	return ders, nil
}

func signedByOneOf(crl *x509.RevocationList, cas []*x509.Certificate) bool {
	for _, ca := range cas {
		if string(ca.RawSubject) == string(crl.RawIssuer) && crl.CheckSignatureFrom(ca) == nil {
			return true
		}
	}
	return false
}

// check rejects a certificate chain if any certificate in any of its verified
// chains is listed, so revoking an intermediate CA refuses everything it
// issued. It also rejects every chain once a list has gone stale: after
// NextUpdate the list may be missing revocations, so the agent fails closed
// instead of trusting it.
func (l *revocationList) check(chains [][]*x509.Certificate, now time.Time) error {
	if l == nil {
		return nil
	}
	if !l.nextUpdate.IsZero() && !now.Before(l.nextUpdate) {
		return fmt.Errorf("certificate revocation list expired at %s", l.nextUpdate.UTC().Format(time.RFC3339))
	}
	for _, chain := range chains {
		for _, cert := range chain {
			if _, revoked := l.revoked[revocationKey(cert.RawIssuer, cert.SerialNumber)]; revoked {
				return fmt.Errorf("certificate %q (serial %s) in the client's chain has been revoked", cert.Subject.CommonName, cert.SerialNumber)
			}
		}
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
