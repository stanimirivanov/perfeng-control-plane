// Package objectstore verifies immutable artifact bytes read from S3-compatible storage.
package objectstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"net/url"
	"path"
	"strings"

	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

var (
	// ErrObjectNotFound classifies an absent object independently of invalid evidence.
	ErrObjectNotFound = errors.New("artifact object not found")
	// ErrObjectMismatch means stored bytes or metadata do not match their immutable reference.
	ErrObjectMismatch = errors.New("artifact object does not match its reference")
)

const maximumReadSize int64 = 256 << 20

// Object is one S3 response. SizeBytes and MediaType are trusted only after the
// reader compares them with the immutable artifact reference and actual bytes.
type Object struct {
	Body      io.ReadCloser
	SizeBytes int64
	MediaType string
}

// Getter is the narrow S3-compatible client boundary. Implementations must honor
// context cancellation, classify missing keys with ErrObjectNotFound, and return
// safe errors that do not expose credentials or signed requests.
type Getter interface {
	GetObject(context.Context, string, string) (Object, error)
}

// Reader restricts artifact locations and returns only checksum-verified bytes.
type Reader struct {
	getter   Getter
	bucket   string
	maxBytes int64
}

// NewReader configures one approved bucket and a strict positive read limit.
func NewReader(getter Getter, bucket string, maxBytes int64) (*Reader, error) {
	if getter == nil || !validBucket(bucket) || maxBytes < 1 || maxBytes > maximumReadSize {
		return nil, run.ErrValidation
	}

	return &Reader{getter: getter, bucket: bucket, maxBytes: maxBytes}, nil
}

// Read verifies location policy, response metadata, byte count and SHA-256
// before returning an independently owned byte slice.
func (reader *Reader) Read(ctx context.Context, artifact run.Artifact) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	bucket, key, err := reader.location(artifact)
	if err != nil {
		return nil, err
	}
	if artifact.SizeBytes > reader.maxBytes {
		return nil, run.ErrValidation
	}

	object, err := reader.getter.GetObject(ctx, bucket, key)
	if err != nil {
		return nil, err
	}
	if object.Body == nil {
		return nil, ErrObjectMismatch
	}
	if object.SizeBytes != artifact.SizeBytes || object.MediaType != artifact.MediaType {
		return nil, errors.Join(ErrObjectMismatch, object.Body.Close())
	}

	content, err := readAndClose(ctx, object.Body, artifact.SizeBytes)
	if err != nil {
		return nil, err
	}
	if int64(len(content)) != artifact.SizeBytes {
		return nil, ErrObjectMismatch
	}
	digest := sha256.Sum256(content)
	if hex.EncodeToString(digest[:]) != artifact.SHA256 {
		return nil, ErrObjectMismatch
	}

	return content, nil
}

func (reader *Reader) location(artifact run.Artifact) (string, string, error) {
	if artifact.Validate() != nil {
		return "", "", run.ErrValidation
	}
	parsed, err := url.Parse(artifact.URI)
	if err != nil || parsed.Scheme != "s3" || parsed.Host != reader.bucket ||
		parsed.Hostname() != reader.bucket || parsed.RawPath != "" {
		return "", "", run.ErrValidation
	}
	key := strings.TrimPrefix(parsed.Path, "/")
	prefix := "runs/" + artifact.RunID + "/"
	if key == "" || key != path.Clean(key) || strings.Contains(key, "//") ||
		!strings.HasPrefix(key, prefix) || key == prefix {
		return "", "", run.ErrValidation
	}

	return reader.bucket, key, nil
}

func readAndClose(ctx context.Context, body io.ReadCloser, expected int64) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, errors.Join(err, body.Close())
	}
	content, readErr := io.ReadAll(io.LimitReader(body, expected+1))
	closeErr := body.Close()
	if readErr != nil {
		return nil, errors.Join(readErr, closeErr)
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return nil, errors.Join(contextErr, closeErr)
	}
	if closeErr != nil {
		return nil, closeErr
	}

	return content, nil
}

func validBucket(bucket string) bool {
	if len(bucket) < 3 || len(bucket) > 63 || net.ParseIP(bucket) != nil ||
		!asciiLowerOrDigit(bucket[0]) || !asciiLowerOrDigit(bucket[len(bucket)-1]) ||
		strings.Contains(bucket, "..") || strings.Contains(bucket, ".-") ||
		strings.Contains(bucket, "-.") {
		return false
	}
	for _, character := range bucket {
		if (character < 'a' || character > 'z') &&
			(character < '0' || character > '9') && character != '.' && character != '-' {
			return false
		}
	}

	return true
}

func asciiLowerOrDigit(character byte) bool {
	return (character >= 'a' && character <= 'z') ||
		(character >= '0' && character <= '9')
}
