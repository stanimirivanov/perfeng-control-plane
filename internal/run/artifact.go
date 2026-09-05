package run

import (
	"context"
	"errors"
	"net/url"
	"regexp"
	"strings"

	"github.com/stanimirivanov/perfeng-control-plane/internal/contract"
)

// ErrArtifactConflict identifies an artifact ID already bound to different evidence.
var ErrArtifactConflict = errors.New("artifact identity already bound to different evidence")

// Artifact records a verified object reference, not object bytes or a claim
// that collection/analysis has completed. Matches artifact/v1 fields.
type Artifact struct {
	ID        string `json:"id"`
	RunID     string `json:"runId"`
	Kind      string `json:"kind"`
	URI       string `json:"uri"`
	SHA256    string `json:"sha256"`
	SizeBytes int64  `json:"sizeBytes"`
	MediaType string `json:"mediaType"`
	Format    string `json:"format"`
}

var (
	artifactID     = regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`)
	artifactHash   = regexp.MustCompile(`^[a-f0-9]{64}$`)
	artifactURI    = regexp.MustCompile(`^(s3|https)://[^\s?#]+$`)
	artifactMedia  = regexp.MustCompile(`^[a-z0-9][a-z0-9!#$&^_.+-]*/[a-z0-9][a-z0-9!#$&^_.+-]*$`)
	artifactFormat = regexp.MustCompile(`^[a-z0-9][a-z0-9./_-]*$`)
)

// Validate checks the artifact/v1 shape and excludes mutable or credential-bearing URLs.
func (a Artifact) Validate() error {
	if !artifactID.MatchString(a.ID) || !contract.ValidID(a.RunID) ||
		(a.Kind != "raw" && a.Kind != "normalized") || !artifactHash.MatchString(a.SHA256) ||
		a.SizeBytes < 0 || !artifactMedia.MatchString(a.MediaType) ||
		!artifactFormat.MatchString(a.Format) || !artifactURI.MatchString(a.URI) {
		return ErrValidation
	}
	u, err := url.Parse(a.URI)
	// Additional storage policy: never persist URL credentials or presigned URLs.
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" ||
		u.Fragment != "" || strings.ContainsAny(a.URI, "\r\n\t") {
		return ErrValidation
	}

	return nil
}

// ArtifactRepository stores references separately from Run snapshots.
// Registration is a worker-only, immutable and idempotent operation. Listing is
// principal-scoped and ordered by artifact ID for both worker recovery and the
// read-only HTTP collection. An owned Run without evidence returns an empty
// slice; an invisible Run returns ErrNotFound. The collector must verify bytes,
// checksum, approved storage and ownership before registration. These methods
// neither upload nor fetch objects.
type ArtifactRepository interface {
	// RegisterArtifact stores an already verified reference idempotently. A
	// different binding for the same ID returns ErrArtifactConflict.
	RegisterArtifact(ctx context.Context, principal string, artifact Artifact) error

	// GetArtifact returns one reference belonging to a principal-visible Run.
	GetArtifact(ctx context.Context, principal, runID, artifactID string) (Artifact, error)

	// ListArtifacts returns a stable artifact-ID-ordered slice. An owned Run
	// without evidence returns an empty slice; an invisible Run returns ErrNotFound.
	ListArtifacts(ctx context.Context, principal, runID string) ([]Artifact, error)
}
