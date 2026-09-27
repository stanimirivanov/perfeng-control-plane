package objectstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"hash"
	"io"

	"github.com/stanimirivanov/perfeng-control-plane/internal/nativeworker"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

var _ nativeworker.ArtifactVerifier = (*NativeUploadVerifier)(nil)

// NativeUploadVerifier streams one server-selected native upload and accepts
// only exact response metadata, byte count and SHA-256.
type NativeUploadVerifier struct {
	getter Getter
	bucket string
}

// NewNativeUploadVerifier configures verification for one approved S3 bucket.
func NewNativeUploadVerifier(getter Getter, bucket string) (*NativeUploadVerifier, error) {
	if getter == nil || !validBucket(bucket) {
		return nil, run.ErrValidation
	}

	return &NativeUploadVerifier{getter: getter, bucket: bucket}, nil
}

// VerifyNativeArtifact streams and verifies the candidate without retaining
// its bytes in control-plane memory.
func (verifier *NativeUploadVerifier) VerifyNativeArtifact(
	ctx context.Context,
	candidate nativeworker.ArtifactCandidate,
) (nativeworker.VerifiedArtifact, error) {
	if err := ctx.Err(); err != nil {
		return nativeworker.VerifiedArtifact{}, err
	}
	if verifier == nil || verifier.getter == nil || !validBucket(verifier.bucket) ||
		!candidate.Valid() {
		return nativeworker.VerifiedArtifact{}, run.ErrValidation
	}

	reservation := candidate.Reservation
	object, err := verifier.getter.GetObject(ctx, verifier.bucket, reservation.ObjectKey)
	if err != nil {
		if errors.Is(err, ErrObjectNotFound) {
			return nativeworker.VerifiedArtifact{},
				errors.Join(nativeworker.ErrArtifactVerification, err)
		}
		return nativeworker.VerifiedArtifact{}, err
	}
	if object.Body == nil {
		return nativeworker.VerifiedArtifact{},
			errors.Join(nativeworker.ErrArtifactVerification, ErrObjectMismatch)
	}
	if object.SizeBytes != reservation.Size || object.MediaType != reservation.MediaType {
		return nativeworker.VerifiedArtifact{}, errors.Join(
			nativeworker.ErrArtifactVerification,
			ErrObjectMismatch,
			object.Body.Close(),
		)
	}
	if err = verifyNativeUploadBody(ctx, object.Body, reservation.Size, reservation.SHA256); err != nil {
		if errors.Is(err, ErrObjectMismatch) {
			return nativeworker.VerifiedArtifact{},
				errors.Join(nativeworker.ErrArtifactVerification, err)
		}
		return nativeworker.VerifiedArtifact{}, err
	}

	verified := nativeworker.VerifiedArtifact{
		Candidate: candidate,
		URI:       "s3://" + verifier.bucket + "/" + reservation.ObjectKey,
	}
	if !verified.Valid() {
		return nativeworker.VerifiedArtifact{}, errors.New("invalid verified native artifact")
	}

	return verified, nil
}

func verifyNativeUploadBody(
	ctx context.Context,
	body io.ReadCloser,
	expectedSize int64,
	expectedSHA256 string,
) error {
	digest := sha256.New()
	written, readErr := io.Copy(digest, io.LimitReader(body, expectedSize+1))
	closeErr := body.Close()
	if readErr != nil {
		return errors.Join(readErr, closeErr)
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return errors.Join(contextErr, closeErr)
	}
	if closeErr != nil {
		return closeErr
	}
	if written != expectedSize || nativeUploadDigest(digest) != expectedSHA256 {
		return ErrObjectMismatch
	}

	return nil
}

func nativeUploadDigest(digest hash.Hash) string {
	return hex.EncodeToString(digest.Sum(nil))
}
