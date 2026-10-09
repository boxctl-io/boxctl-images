// Package objectstore presigns short-lived, single-object PUT/GET URLs
// against an S3-compatible bucket (Cloudflare R2 in production) --
// see docs/plans/s3-transfer.md. This is the *only* place in this
// codebase that holds the bucket's long-term credentials: callers here
// (cmd/server's export/import handlers) hand out presigned URLs to the
// agent and to end clients, neither of which ever sees the underlying
// access key, mirroring how internal/auth's terminal tickets hand out a
// short-lived capability instead of the dashboard's own bearer token.
//
// cmd/boxctl-image (the image pipeline, in CI) also uses it directly --
// Head/Get/Put/Copy -- with its own write key on the images bucket.
package objectstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
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

// Config is one bucket on an S3-compatible store.
type Config struct {
	// Endpoint is the store's base URL, e.g.
	// https://{accountID}.r2.cloudflarestorage.com or http://minio:9000.
	Endpoint string
	// Region goes into every request's signature. Empty means "auto",
	// the literal value R2 expects -- it has no concept of AWS regions.
	// A store that checks it (MinIO with a region configured, Ceph, AWS)
	// needs its own.
	Region string
	Bucket string
	// PathStyle addresses the bucket as {endpoint}/{bucket}/{key} instead
	// of the default {bucket}.{endpoint}/{key}. R2 and AWS answer the
	// default; a self-hosted store usually has no wildcard DNS name for
	// its buckets, so it needs this.
	PathStyle       bool
	AccessKeyID     string
	SecretAccessKey string
}

// R2Endpoint is the endpoint of an R2 account's buckets.
func R2Endpoint(accountID string) string {
	return fmt.Sprintf("https://%s.r2.cloudflarestorage.com", accountID)
}

// New builds a Store against an R2 bucket, named by its account ID.
//
// Deliberately takes bucket/accountID/keys as plain strings rather than
// reading the environment itself -- cmd/server's caller does that (same
// pattern as every other env-configured piece there, e.g.
// BOXCTL_DASHBOARD_API_TOKEN), so this package stays testable against a
// hand-built Store without env mutation.
func New(accountID, bucket, accessKeyID, secretAccessKey string) (*Store, error) {
	if accountID == "" {
		return nil, fmt.Errorf("objectstore: accountID, bucket, accessKeyID, and secretAccessKey are all required")
	}
	return Open(Config{Endpoint: R2Endpoint(accountID), Bucket: bucket, AccessKeyID: accessKeyID, SecretAccessKey: secretAccessKey})
}

// Open builds a Store from cfg -- any S3-compatible store, not only R2.
func Open(cfg Config) (*Store, error) {
	if cfg.Endpoint == "" || cfg.Bucket == "" || cfg.AccessKeyID == "" || cfg.SecretAccessKey == "" {
		return nil, fmt.Errorf("objectstore: endpoint, bucket, accessKeyID, and secretAccessKey are all required")
	}
	if u, err := url.Parse(cfg.Endpoint); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("objectstore: endpoint %q is not an http(s) URL", cfg.Endpoint)
	}
	region := cfg.Region
	if region == "" {
		region = "auto"
	}

	client := s3.New(s3.Options{
		Region:       region,
		BaseEndpoint: aws.String(cfg.Endpoint),
		UsePathStyle: cfg.PathStyle,
		Credentials:  credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.SecretAccessKey, ""),
		// Only when an operation requires one: the SDK's default adds
		// CRC checksum headers to every upload, which S3-compatible
		// stores don't all accept, and Put's body is already covered by
		// SigV4's payload hash.
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
	})

	return &Store{
		bucket:  cfg.Bucket,
		client:  client,
		presign: s3.NewPresignClient(client),
	}, nil
}

// PresignPut returns a URL an unauthenticated caller (the agent for
// export, the boxctl/boxctl-web client for import) can PUT the object's
// raw bytes to directly, valid for expiry. A single presigned PUT tops
// out at S3's (and R2's) 5 GiB single-object limit -- R2 answers a bigger
// one with EntityTooLarge. Backups past that go up as a multipart upload
// instead (CreateMultipart/PresignUploadPart; see cmd/server's
// runBackupJob).
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

// ErrNotFound is returned by Head and Get for an object that doesn't
// exist.
var ErrNotFound = errors.New("objectstore: object not found")

// Object is what Head reports about one object.
type Object struct {
	Size int64
	// Metadata is the object's x-amz-meta-* headers, keys lowercased
	// without the prefix (R2 and S3 both lowercase them).
	Metadata map[string]string
}

// Head returns key's size and user metadata, or ErrNotFound.
func (s *Store) Head(ctx context.Context, key string) (Object, error) {
	out, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		var nf *types.NotFound
		var nk *types.NoSuchKey
		if errors.As(err, &nf) || errors.As(err, &nk) {
			return Object{}, ErrNotFound
		}
		return Object{}, fmt.Errorf("objectstore: head %s: %w", key, err)
	}
	return Object{Size: aws.ToInt64(out.ContentLength), Metadata: out.Metadata}, nil
}

// Get returns key's whole body, or ErrNotFound. For small objects (the
// image catalog's manifests) -- it reads everything into memory.
func (s *Store) Get(ctx context.Context, key string) ([]byte, error) {
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		var nk *types.NoSuchKey
		if errors.As(err, &nk) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("objectstore: get %s: %w", key, err)
	}
	defer func() { _ = out.Body.Close() }()
	data, err := io.ReadAll(out.Body)
	if err != nil {
		return nil, fmt.Errorf("objectstore: get %s: %w", key, err)
	}
	return data, nil
}

// Put uploads body as key in a single request, with the given content
// type and user metadata. body must be seekable so SigV4 can hash it
// before sending; a single PUT tops out at 5 GiB (see PresignPut).
func (s *Store) Put(ctx context.Context, key string, body io.ReadSeeker, size int64, contentType string, metadata map[string]string) error {
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(s.bucket),
		Key:           aws.String(key),
		Body:          body,
		ContentLength: aws.Int64(size),
		ContentType:   aws.String(contentType),
		Metadata:      metadata,
	})
	if err != nil {
		return fmt.Errorf("objectstore: put %s: %w", key, err)
	}
	return nil
}

// Copy copies src to dst within the bucket, server side -- nothing passes
// through this process -- replacing dst's user metadata with metadata.
func (s *Store) Copy(ctx context.Context, src, dst string, metadata map[string]string) error {
	_, err := s.client.CopyObject(ctx, &s3.CopyObjectInput{
		Bucket:            aws.String(s.bucket),
		Key:               aws.String(dst),
		CopySource:        aws.String(s.bucket + "/" + src),
		Metadata:          metadata,
		MetadataDirective: types.MetadataDirectiveReplace,
	})
	if err != nil {
		return fmt.Errorf("objectstore: copy %s to %s: %w", src, dst, err)
	}
	return nil
}

// List returns every key under prefix, following pagination.
func (s *Store) List(ctx context.Context, prefix string) ([]string, error) {
	var keys []string
	p := s3.NewListObjectsV2Paginator(s.client, &s3.ListObjectsV2Input{
		Bucket: aws.String(s.bucket),
		Prefix: aws.String(prefix),
	})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("objectstore: list %s: %w", prefix, err)
		}
		for _, o := range page.Contents {
			keys = append(keys, aws.ToString(o.Key))
		}
	}
	return keys, nil
}

// MaxParts is S3's (and R2's) cap on parts in one multipart upload.
const MaxParts = 10000

// Part is one uploaded part of a multipart upload, as CompleteMultipart
// needs it: its 1-based number and the ETag R2 returned for it.
type Part struct {
	Number int32  `json:"number"`
	ETag   string `json:"etag"`
}

// CreateMultipart starts a multipart upload for key and returns its
// upload ID -- the way past a single PUT's 5 GiB cap (see PresignPut).
// The caller must end it with CompleteMultipart or AbortMultipart: an
// unfinished upload's parts sit in the bucket, invisible to List, until
// a lifecycle rule (if any) aborts it.
func (s *Store) CreateMultipart(ctx context.Context, key string) (string, error) {
	out, err := s.client.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return "", fmt.Errorf("objectstore: create multipart %s: %w", key, err)
	}
	return aws.ToString(out.UploadId), nil
}

// PresignUploadPart is PresignPut for one part of a multipart upload:
// the caller PUTs that part's raw bytes to it and keeps the response's
// ETag header for CompleteMultipart. R2 requires every part but the last
// to be the same size, and at least 5 MiB.
func (s *Store) PresignUploadPart(ctx context.Context, key, uploadID string, partNumber int32, expiry time.Duration) (string, error) {
	req, err := s.presign.PresignUploadPart(ctx, &s3.UploadPartInput{
		Bucket:     aws.String(s.bucket),
		Key:        aws.String(key),
		UploadId:   aws.String(uploadID),
		PartNumber: aws.Int32(partNumber),
	}, s3.WithPresignExpires(expiry))
	if err != nil {
		return "", fmt.Errorf("objectstore: presign upload part %d of %s: %w", partNumber, key, err)
	}
	return req.URL, nil
}

// CompleteMultipart assembles parts (in order) into key.
func (s *Store) CompleteMultipart(ctx context.Context, key, uploadID string, parts []Part) error {
	completed := make([]types.CompletedPart, len(parts))
	for i, p := range parts {
		completed[i] = types.CompletedPart{PartNumber: aws.Int32(p.Number), ETag: aws.String(p.ETag)}
	}
	_, err := s.client.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
		Bucket:          aws.String(s.bucket),
		Key:             aws.String(key),
		UploadId:        aws.String(uploadID),
		MultipartUpload: &types.CompletedMultipartUpload{Parts: completed},
	})
	if err != nil {
		return fmt.Errorf("objectstore: complete multipart %s: %w", key, err)
	}
	return nil
}

// AbortMultipart discards a multipart upload and any parts already
// uploaded to it.
func (s *Store) AbortMultipart(ctx context.Context, key, uploadID string) error {
	_, err := s.client.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{
		Bucket:   aws.String(s.bucket),
		Key:      aws.String(key),
		UploadId: aws.String(uploadID),
	})
	if err != nil {
		return fmt.Errorf("objectstore: abort multipart %s: %w", key, err)
	}
	return nil
}
