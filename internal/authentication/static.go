// Package authentication verifies credentials at the HTTP trust boundary.
package authentication

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/stanimirivanov/perfeng-control-plane/internal/httpapi"
	"github.com/stanimirivanov/perfeng-control-plane/internal/jsondocument"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

const (
	maximumCredentialFileBytes = 64 << 10
	maximumCredentials         = 100
	maximumPrincipalBytes      = 256
	maximumTokenBytes          = 4096
	permissionCount            = 6
)

const (
	permissionCreateRun          = "runs:create"
	permissionReadRun            = "runs:read"
	permissionCancelRun          = "runs:cancel"
	permissionCreateBaseline     = "baselines:create"
	permissionReadBaseline       = "baselines:read"
	permissionTransitionBaseline = "baselines:transition"
)

// ErrUnauthenticated identifies a credential that is absent or not accepted.
var ErrUnauthenticated = errors.New("unauthenticated")

type credentialDocument struct {
	SchemaVersion int                `json:"schemaVersion"`
	Credentials   []credentialConfig `json:"credentials"`
}

type credentialConfig struct {
	TokenSHA256 string   `json:"tokenSha256"`
	Principal   string   `json:"principal"`
	Permissions []string `json:"permissions"`
}

// Static verifies bearer tokens against an immutable, hashed credential set.
// It is safe for concurrent use.
type Static struct {
	identities map[[sha256.Size]byte]httpapi.Identity
}

// LoadStatic reads one strict credential document and constructs immutable
// authentication state. Configuration failures return run.ErrValidation.
func LoadStatic(source fs.FS, path string) (*Static, error) {
	if source == nil || !validPath(path) {
		return nil, run.ErrValidation
	}
	data, err := readFile(source, path)
	if err != nil || !jsondocument.Valid(data, maximumCredentialFileBytes) {
		return nil, run.ErrValidation
	}
	var document credentialDocument
	if !jsondocument.Decode(data, &document) || document.SchemaVersion != 1 ||
		len(document.Credentials) == 0 || len(document.Credentials) > maximumCredentials {
		return nil, run.ErrValidation
	}

	identities := make(map[[sha256.Size]byte]httpapi.Identity, len(document.Credentials))
	principals := make(map[string]httpapi.Identity, len(document.Credentials))
	for _, configured := range document.Credentials {
		digest, identity, err := configured.values()
		if err != nil {
			return nil, err
		}
		if _, exists := identities[digest]; exists {
			return nil, run.ErrValidation
		}
		if existing, exists := principals[identity.Principal]; exists && existing != identity {
			return nil, run.ErrValidation
		}
		identities[digest] = identity
		principals[identity.Principal] = identity
	}

	return &Static{identities: identities}, nil
}

// Authenticate verifies an opaque token and returns its configured stable
// principal and operation permissions.
func (authenticator *Static) Authenticate(
	ctx context.Context,
	token string,
) (httpapi.Identity, error) {
	if err := ctx.Err(); err != nil {
		return httpapi.Identity{}, err
	}
	if authenticator == nil || len(token) == 0 || len(token) > maximumTokenBytes ||
		strings.ContainsAny(token, " \t\r\n,") {
		return httpapi.Identity{}, ErrUnauthenticated
	}

	identity, exists := authenticator.identities[sha256.Sum256([]byte(token))]
	if !exists {
		return httpapi.Identity{}, ErrUnauthenticated
	}

	return identity, nil
}

func (configured credentialConfig) values() (
	[sha256.Size]byte,
	httpapi.Identity,
	error,
) {
	var digest [sha256.Size]byte
	decoded, err := hex.DecodeString(configured.TokenSHA256)
	if err != nil || len(decoded) != sha256.Size ||
		configured.TokenSHA256 != strings.ToLower(configured.TokenSHA256) ||
		!validPrincipal(configured.Principal) || len(configured.Permissions) == 0 ||
		len(configured.Permissions) > permissionCount {
		return digest, httpapi.Identity{}, run.ErrValidation
	}
	copy(digest[:], decoded)

	identity := httpapi.Identity{Principal: configured.Principal}
	seen := make(map[string]struct{}, len(configured.Permissions))
	for _, permission := range configured.Permissions {
		if _, duplicate := seen[permission]; duplicate || !applyPermission(&identity, permission) {
			return digest, httpapi.Identity{}, run.ErrValidation
		}
		seen[permission] = struct{}{}
	}

	return digest, identity, nil
}

func applyPermission(identity *httpapi.Identity, permission string) bool {
	switch permission {
	case permissionCreateRun:
		identity.CreateRun = true
	case permissionReadRun:
		identity.ReadRun = true
	case permissionCancelRun:
		identity.CancelRun = true
	case permissionCreateBaseline:
		identity.CreateBaseline = true
	case permissionReadBaseline:
		identity.ReadBaseline = true
	case permissionTransitionBaseline:
		identity.TransitionBaseline = true
	default:
		return false
	}

	return true
}

func readFile(source fs.FS, path string) ([]byte, error) {
	file, err := source.Open(path)
	if err != nil {
		return nil, run.ErrValidation
	}
	data, readErr := io.ReadAll(io.LimitReader(file, maximumCredentialFileBytes+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil || len(data) > maximumCredentialFileBytes {
		return nil, run.ErrValidation
	}

	return data, nil
}

func validPath(path string) bool {
	return path != "." && fs.ValidPath(path) && !strings.ContainsRune(path, '\\')
}

func validPrincipal(principal string) bool {
	if !utf8.ValidString(principal) || len(principal) == 0 ||
		len(principal) > maximumPrincipalBytes || strings.TrimSpace(principal) != principal {
		return false
	}
	for _, character := range principal {
		if unicode.IsControl(character) {
			return false
		}
	}

	return true
}
