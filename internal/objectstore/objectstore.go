// Package objectstore presigns short-lived, single-object PUT/GET URLs
// against an S3-compatible bucket (Cloudflare R2 in production) --
// see docs/plans/s3-transfer.md. This is the *only* place in this
// codebase that holds the bucket's long-term credentials: callers here
// (cmd/server's export/import handlers) hand out presigned URLs to the
// agent and to end clients, neither of which ever sees the underlying
// access key, mirroring how internal/auth's terminal tickets hand out a
// short-lived capability instead of the dashboard's own bearer token.
package objectstore

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// Store presigns PUT/GET URLs for one bucket and can delete objects in
// it directly (used for cleanup once a transfer's object is no longer
// needed -- see docs/plans/s3-transfer.md's cleanup section). Built once
// at startup from env config (see New) and shared across requests; the
// underlying s3.Client is safe for concurrent use.
type Store struct {
	bucket  string
	client  *s3.Client
	presign *s3.PresignClient
}

// New builds a Store against an R2 bucket. accountID is R2's
// account-specific subdomain (the bucket's endpoint is
// https://{accountID}.r2.cloudflarestorage.com); region is fixed to
// "auto" per Cloudflare's S3-compatibility docs -- R2 has no concept of
// AWS regions, "auto" is the literal value R2's API expects there.
//
// Deliberately takes bucket/accountID/keys as plain strings rather than
// reading the environment itself -- cmd/server's caller does that (same
// pattern as every other env-configured piece there, e.g.
// BOXCTL_DASHBOARD_API_TOKEN), so this package stays testable against a
// hand-built Store without env mutation.
func New(accountID, bucket, accessKeyID, secretAccessKey string) (*Store, error) {
	if accountID == "" || bucket == "" || accessKeyID == "" || secretAccessKey == "" {
		return nil, fmt.Errorf("objectstore: accountID, bucket, accessKeyID, and secretAccessKey are all required")
	}

	client := s3.New(s3.Options{
		Region:       "auto",
		BaseEndpoint: aws.String(fmt.Sprintf("https://%s.r2.cloudflarestorage.com", accountID)),
		Credentials:  credentials.NewStaticCredentialsProvider(accessKeyID, secretAccessKey, ""),
	})

	return &Store{
		bucket:  bucket,
		client:  client,
		presign: s3.NewPresignClient(client),
	}, nil
}

// PresignPut returns a URL an unauthenticated caller (the agent for
// export, the boxctl/boxctl-web client for import) can PUT the object's
// raw bytes to directly, valid for expiry. A single presigned PUT tops
// out at S3's (and R2's) 5GB single-object limit -- comfortably above the
// bundle sizes docs/plans/vm-snapshot-download.md measured (tens to a
// few hundred MB); revisit with multipart presigning only if that stops
// holding.
func (s *Store) PresignPut(ctx context.Context, key string, expiry time.Duration) (string, error) {
	req, err := s.presign.PresignPutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	}, s3.WithPresignExpires(expiry))
	if err != nil {
		return "", fmt.Errorf("objectstore: presign put %s: %w", key, err)
	}
	return req.URL, nil
}

// PresignGet is PresignPut's download-side counterpart. filename, when
// non-empty, sets the response's Content-Disposition (as
// "attachment; filename=..."), so a browser navigating straight to the
// URL (e.g. after a 302 redirect from cmd/server's handleExportStatus)
// triggers an actual file download with a sensible name instead of
// trying to render R2's response inline or saving it under the bucket
// key's own name -- R2/S3 both let a presigned GET override this
// per-request via response-content-disposition without the object
// itself needing that header set at upload time. Pass "" for a
// caller that doesn't need this (e.g. the agent's own GET-and-Patch
// during import, which never touches a browser).
func (s *Store) PresignGet(ctx context.Context, key string, expiry time.Duration, filename string) (string, error) {
	input := &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	}
	if filename != "" {
		input.ResponseContentDisposition = aws.String(fmt.Sprintf("attachment; filename=%q", filename))
		input.ResponseContentType = aws.String("application/zstd")
	}
	req, err := s.presign.PresignGetObject(ctx, input, s3.WithPresignExpires(expiry))
	if err != nil {
		return "", fmt.Errorf("objectstore: presign get %s: %w", key, err)
	}
	return req.URL, nil
}

// Size returns key's exact object size in the bucket -- used to answer
// "how big is this download" (see cmd/server's handleExportStatus)
// without the client ever HEADing the presigned GET URL itself: R2/S3's
// presigned-URL signature covers the HTTP method, so a URL presigned for
// GET rejects a HEAD against it with 403 (confirmed live) -- HeadObject
// with this Store's own real credentials has no such restriction.
func (s *Store) Size(ctx context.Context, key string) (int64, error) {
	out, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return 0, fmt.Errorf("objectstore: head %s: %w", key, err)
	}
	if out.ContentLength == nil {
		return 0, fmt.Errorf("objectstore: head %s: no content-length in response", key)
	}
	return *out.ContentLength, nil
}

// Delete removes key from the bucket -- called once a transfer's object
// is known to no longer be needed (see docs/plans/s3-transfer.md's
// cleanup section for which side does this for export vs. import). Not
// finding the object is not treated as an error by S3-compatible
// DeleteObject semantics, so a caller that races a bucket lifecycle rule
// (the backstop for paths that don't call this explicitly) never sees a
// spurious failure here.
func (s *Store) Delete(ctx context.Context, key string) error {
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return fmt.Errorf("objectstore: delete %s: %w", key, err)
	}
	return nil
}
