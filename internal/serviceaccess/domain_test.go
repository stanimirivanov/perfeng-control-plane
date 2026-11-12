package serviceaccess

import (
	"strings"
	"testing"
)

func TestIdentityValidationPreservesExactBoundedIdentity(t *testing.T) {
	for _, identity := range []Identity{
		{Issuer: "urn:people", ID: "maya"},
		{Issuer: "https://identity.example", ID: strings.Repeat("界", 256)},
	} {
		if !identity.Valid() {
			t.Fatal("valid identity rejected", identity)
		}
	}
	for _, identity := range []Identity{
		{}, {Issuer: "people", ID: "maya"}, {Issuer: "urn:people space", ID: "maya"},
		{Issuer: "urn:people", ID: " \t\n"}, {Issuer: "urn:people", ID: "nul\x00"},
		{Issuer: "urn:people", ID: strings.Repeat("界", 257)},
		{Issuer: "urn:people", ID: "\xff"},
	} {
		if identity.Valid() {
			t.Fatal("invalid identity accepted")
		}
	}
}

func TestPermissionVocabularyIsClosed(t *testing.T) {
	for _, permission := range []Permission{ReadRun, CreateRun, CancelRun, Rerun, CreateBaseline, QualifyBaseline, ApproveBaseline, RetireBaseline} {
		if !permission.Valid() {
			t.Fatal("contract right rejected", permission)
		}
	}
	for _, permission := range []Permission{"", "baseline:write", "run:*", "run:read run:rerun"} {
		if permission.Valid() {
			t.Fatal("unreviewed right accepted", permission)
		}
	}
}
