// Package serviceaccess defines service ownership and independent grant vocabulary.
// It does not verify credentials or authorize a mutation outside its transaction.
package serviceaccess

import (
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Identity is an exact authority-issued service or actor identity, never a URL to fetch.
type Identity struct {
	Issuer string
	ID     string
}

// Valid rejects unbounded, non-URI or unrepresentable persistence identities.
func (identity Identity) Valid() bool {
	if !utf8.ValidString(identity.Issuer) || !utf8.ValidString(identity.ID) ||
		utf8.RuneCountInString(identity.Issuer) > 512 || utf8.RuneCountInString(identity.ID) > 256 {
		return false
	}
	if strings.TrimSpace(identity.ID) == "" || strings.ContainsRune(identity.Issuer+identity.ID, 0) {
		return false
	}

	issuer, err := url.Parse(identity.Issuer)
	return err == nil && issuer.Scheme != "" && !strings.ContainsFunc(identity.Issuer, unicode.IsSpace)
}

// Permission identifies one independent service right; none implies another.
type Permission string

const (
	// ReadRun allows exact Run/evidence/baseline observations, not mutations.
	ReadRun Permission = "run:read"
	// CreateRun allows new Run admission.
	CreateRun Permission = "run:create"
	// CancelRun allows Run cancellation.
	CancelRun Permission = "run:cancel"
	// Rerun allows the separately admitted attributable rerun operation.
	Rerun Permission = "run:rerun"
	// CreateBaseline allows baseline candidate creation.
	CreateBaseline Permission = "baseline:create"
	// QualifyBaseline allows qualification under existing evidence rules.
	QualifyBaseline Permission = "baseline:qualify"
	// ApproveBaseline allows explicit baseline approval.
	ApproveBaseline Permission = "baseline:approve"
	// RetireBaseline allows explicit baseline retirement.
	RetireBaseline Permission = "baseline:retire"
)

// Valid recognizes only the contract's closed permission vocabulary.
func (permission Permission) Valid() bool {
	switch permission {
	case ReadRun, CreateRun, CancelRun, Rerun, CreateBaseline, QualifyBaseline, ApproveBaseline, RetireBaseline:
		return true
	}

	return false
}

// Binding preserves reviewed service and original creator independently of a reader.
// OwnerPrincipal is the legacy native owner; it is not a service grant or human identity.
type Binding struct {
	Service, Creator Identity
	CreatorKind      string
	OwnerPrincipal   string
	ReviewReference  string
}
