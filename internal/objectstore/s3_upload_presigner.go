package objectstore

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsv4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/stanimirivanov/perfeng-control-plane/internal/nativeworker"
	"github.com/stanimirivanov/perfeng-control-plane/internal/run"
)

var s3SignaturePattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

// S3PutObjectPresignAPI is the single AWS SDK operation required to authorize
// direct native artifact upload. *s3.PresignClient satisfies this interface.
type S3PutObjectPresignAPI interface {
	// PresignPutObject signs one PUT request without sending it to object storage.
	PresignPutObject(
		context.Context,
		*s3.PutObjectInput,
		...func(*s3.PresignOptions),
	) (*awsv4.PresignedHTTPRequest, error)
}

// S3UploadPresigner adapts AWS SigV4 presigning to the native upload boundary.
// It returns only the contract's closed header set and never retains generated
// delivery credentials.
type S3UploadPresigner struct {
	client S3PutObjectPresignAPI
	bucket string
	now    func() time.Time
}

var _ nativeworker.UploadSigner = (*S3UploadPresigner)(nil)
var _ S3PutObjectPresignAPI = (*s3.PresignClient)(nil)

// NewS3UploadPresigner requires a configured AWS SDK presign client and the
// reviewed artifact bucket. Endpoint, credentials, region and path style belong
// to client composition.
func NewS3UploadPresigner(
	client S3PutObjectPresignAPI,
	bucket string,
) (*S3UploadPresigner, error) {
	return newS3UploadPresigner(client, bucket, time.Now)
}

func newS3UploadPresigner(
	client S3PutObjectPresignAPI,
	bucket string,
	now func() time.Time,
) (*S3UploadPresigner, error) {
	if client == nil || !validBucket(bucket) || now == nil {
		return nil, run.ErrValidation
	}

	return &S3UploadPresigner{client: client, bucket: bucket, now: now}, nil
}

// PresignNativeUpload creates a short-lived PUT for the exact reservation key,
// media type and full-object checksum. The resulting URL expiry is parsed and
// checked against durable lease-bounded authority before it is returned.
func (presigner *S3UploadPresigner) PresignNativeUpload(
	ctx context.Context,
	reservation nativeworker.UploadReservation,
) (nativeworker.UploadAuthorization, error) {
	if presigner == nil || presigner.client == nil || !validBucket(presigner.bucket) ||
		presigner.now == nil || !reservation.Valid() {
		return nativeworker.UploadAuthorization{}, run.ErrValidation
	}
	if err := ctx.Err(); err != nil {
		return nativeworker.UploadAuthorization{}, err
	}

	expires := reservation.ExpiresAt.Sub(presigner.now().UTC()).Truncate(time.Second)
	if expires < time.Second {
		return nativeworker.UploadAuthorization{}, nativeworker.ErrLeaseGone
	}
	if expires > time.Second {
		expires -= time.Second
	}
	checksum, err := hex.DecodeString(reservation.SHA256)
	if err != nil {
		return nativeworker.UploadAuthorization{}, run.ErrValidation
	}
	encodedChecksum := base64.StdEncoding.EncodeToString(checksum)
	output, err := presigner.client.PresignPutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(presigner.bucket), Key: aws.String(reservation.ObjectKey),
		ContentType: aws.String(reservation.MediaType), ChecksumSHA256: aws.String(encodedChecksum),
	}, func(options *s3.PresignOptions) {
		options.Expires = expires
	})
	if err != nil {
		return nativeworker.UploadAuthorization{}, classifyUploadSigningError(ctx, err)
	}
	authorization, valid := nativeUploadAuthorization(output, presigner.bucket, reservation)
	if !valid || !authorization.Valid(reservation) {
		return nativeworker.UploadAuthorization{}, run.ErrUnavailable
	}

	return authorization, nil
}

func nativeUploadAuthorization(
	output *awsv4.PresignedHTTPRequest,
	bucket string,
	reservation nativeworker.UploadReservation,
) (nativeworker.UploadAuthorization, bool) {
	if output == nil || output.Method != http.MethodPut {
		return nativeworker.UploadAuthorization{}, false
	}
	parsed, err := url.ParseRequestURI(output.URL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil ||
		parsed.Fragment != "" || !validPresignedUploadTarget(parsed, bucket, reservation.ObjectKey) {
		return nativeworker.UploadAuthorization{}, false
	}
	expiresAt, valid := signedUploadExpiry(parsed.Query())
	if !valid || expiresAt.After(reservation.ExpiresAt) {
		return nativeworker.UploadAuthorization{}, false
	}
	headers, valid := closedUploadHeaders(output.SignedHeader, parsed.Host)
	if !valid {
		return nativeworker.UploadAuthorization{}, false
	}

	return nativeworker.UploadAuthorization{
		URL: output.URL, Headers: headers, Method: output.Method, ExpiresAt: expiresAt,
	}, true
}

func validPresignedUploadTarget(parsed *url.URL, bucket, objectKey string) bool {
	pathStyle := parsed.Path == "/"+bucket+"/"+objectKey
	virtualHosted := parsed.Path == "/"+objectKey &&
		strings.HasPrefix(parsed.Hostname(), bucket+".")

	return pathStyle || virtualHosted
}

func signedUploadExpiry(query url.Values) (time.Time, bool) {
	algorithm, algorithmOK := singleQueryValue(query, "X-Amz-Algorithm")
	credential, credentialOK := singleQueryValue(query, "X-Amz-Credential")
	date, dateOK := singleQueryValue(query, "X-Amz-Date")
	expires, expiresOK := singleQueryValue(query, "X-Amz-Expires")
	signature, signatureOK := singleQueryValue(query, "X-Amz-Signature")
	signedHeaders, signedHeadersOK := singleQueryValue(query, "X-Amz-SignedHeaders")
	operation, operationOK := singleQueryValue(query, "x-id")
	if !algorithmOK || algorithm != "AWS4-HMAC-SHA256" || !credentialOK || credential == "" ||
		!dateOK || !expiresOK || !signatureOK || !s3SignaturePattern.MatchString(signature) ||
		!signedHeadersOK || signedHeaders != "content-type;host;x-amz-checksum-sha256" ||
		!operationOK || operation != "PutObject" {
		return time.Time{}, false
	}
	signedAt, err := time.Parse("20060102T150405Z", date)
	if err != nil {
		return time.Time{}, false
	}
	seconds, err := strconv.Atoi(expires)
	if err != nil || seconds < 1 || seconds > 300 {
		return time.Time{}, false
	}

	return signedAt.Add(time.Duration(seconds) * time.Second).UTC(), true
}

func singleQueryValue(values url.Values, key string) (string, bool) {
	items, exists := values[key]
	returnValue := ""
	if exists && len(items) == 1 {
		returnValue = items[0]
	}

	return returnValue, exists && len(items) == 1
}

func closedUploadHeaders(headers http.Header, host string) (nativeworker.UploadHeaders, bool) {
	if len(headers) != 3 {
		return nativeworker.UploadHeaders{}, false
	}
	contentType, contentTypeOK := singleHeaderValue(headers, "Content-Type")
	checksum, checksumOK := singleHeaderValue(headers, "X-Amz-Checksum-Sha256")
	signedHost, hostOK := singleHeaderValue(headers, "Host")
	if !contentTypeOK || !checksumOK || !hostOK || signedHost != host {
		return nativeworker.UploadHeaders{}, false
	}

	return nativeworker.UploadHeaders{
		ContentType: contentType, ChecksumSHA256: checksum,
	}, true
}

func singleHeaderValue(headers http.Header, name string) (string, bool) {
	for key, values := range headers {
		if strings.EqualFold(key, name) {
			returnValue := ""
			if len(values) == 1 {
				returnValue = values[0]
			}

			return returnValue, len(values) == 1
		}
	}

	return "", false
}

func classifyUploadSigningError(ctx context.Context, err error) error {
	if contextErr := ctx.Err(); contextErr != nil {
		return contextErr
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}

	return run.ErrUnavailable
}
