package objectstore

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"
)

// Presigning is pure local computation (SigV4 over the request, no
// network round trip) -- these tests exercise New/PresignPut/PresignGet
// against fake credentials and assert on the URL shape, without needing
// a real R2 bucket or any network access.

func TestNew_RequiresAllFields(t *testing.T) {
	cases := []struct {
		name, accountID, bucket, accessKeyID, secretAccessKey string
	}{
		{"missing accountID", "", "bucket", "key", "secret"},
		{"missing bucket", "acct", "", "key", "secret"},
		{"missing accessKeyID", "acct", "bucket", "", "secret"},
		{"missing secretAccessKey", "acct", "bucket", "key", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := New(c.accountID, c.bucket, c.accessKeyID, c.secretAccessKey); err == nil {
				t.Fatal("expected an error, got nil")
			}
		})
	}
}

func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := New("test-account", "test-bucket", "AKIAFAKE", "fakesecret")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

func TestPresignPut(t *testing.T) {
	s := testStore(t)
	rawURL, err := s.PresignPut(context.Background(), "exports/owner/vm-123.tar.zst", 30*time.Minute)
	if err != nil {
		t.Fatalf("PresignPut: %v", err)
	}

	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("presigned URL %q did not parse: %v", rawURL, err)
	}
	if !strings.HasSuffix(u.Host, "test-account.r2.cloudflarestorage.com") {
		t.Errorf("host = %q, want it to target the account's r2.cloudflarestorage.com endpoint", u.Host)
	}
	if !strings.Contains(u.Host, "test-bucket") {
		t.Errorf("host = %q, want it to contain the bucket (virtual-hosted-style addressing)", u.Host)
	}
	if !strings.Contains(u.Path, "exports/owner/vm-123.tar.zst") {
		t.Errorf("path = %q, want it to contain the key", u.Path)
	}
	q := u.Query()
	if q.Get("X-Amz-Signature") == "" {
		t.Error("missing X-Amz-Signature -- URL doesn't look presigned")
	}
	if q.Get("X-Amz-Expires") != "1800" {
		t.Errorf("X-Amz-Expires = %q, want 1800 (30m)", q.Get("X-Amz-Expires"))
	}
}

func TestPresignGet(t *testing.T) {
	s := testStore(t)
	rawURL, err := s.PresignGet(context.Background(), "imports/owner/upload-456.tar.zst", 15*time.Minute, "")
	if err != nil {
		t.Fatalf("PresignGet: %v", err)
	}

	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("presigned URL %q did not parse: %v", rawURL, err)
	}
	if !strings.Contains(u.Path, "imports/owner/upload-456.tar.zst") {
		t.Errorf("path = %q, want it to contain the key", u.Path)
	}
	if u.Query().Get("X-Amz-Signature") == "" {
		t.Error("missing X-Amz-Signature -- URL doesn't look presigned")
	}
	if u.Query().Get("response-content-disposition") != "" {
		t.Error("empty filename should not set response-content-disposition")
	}
}

func TestPresignGet_WithFilename(t *testing.T) {
	s := testStore(t)
	rawURL, err := s.PresignGet(context.Background(), "exports/vm-123.tar.zst", 15*time.Minute, "vm-123.tar.zst")
	if err != nil {
		t.Fatalf("PresignGet: %v", err)
	}

	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("presigned URL %q did not parse: %v", rawURL, err)
	}
	wantDisposition := `attachment; filename="vm-123.tar.zst"`
	if got := u.Query().Get("response-content-disposition"); got != wantDisposition {
		t.Errorf("response-content-disposition = %q, want %q", got, wantDisposition)
	}
	if got := u.Query().Get("response-content-type"); got != "application/zstd" {
		t.Errorf("response-content-type = %q, want application/zstd", got)
	}
}

func TestPresignPutAndGet_DifferentSignatures(t *testing.T) {
	s := testStore(t)
	putURL, err := s.PresignPut(context.Background(), "same-key", time.Minute)
	if err != nil {
		t.Fatalf("PresignPut: %v", err)
	}
	getURL, err := s.PresignGet(context.Background(), "same-key", time.Minute, "")
	if err != nil {
		t.Fatalf("PresignGet: %v", err)
	}
	if putURL == getURL {
		t.Error("PUT and GET presigned URLs for the same key should differ (different HTTP method signed)")
	}
}

func TestPresignUploadPart(t *testing.T) {
	s := testStore(t)
	rawURL, err := s.PresignUploadPart(context.Background(), "backups/vm-1.tar.zst", "upload-abc", 7, time.Hour)
	if err != nil {
		t.Fatalf("PresignUploadPart: %v", err)
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("presigned URL %q did not parse: %v", rawURL, err)
	}
	q := u.Query()
	if q.Get("uploadId") != "upload-abc" {
		t.Errorf("uploadId = %q, want upload-abc", q.Get("uploadId"))
	}
	if q.Get("partNumber") != "7" {
		t.Errorf("partNumber = %q, want 7", q.Get("partNumber"))
	}
	if q.Get("X-Amz-Signature") == "" {
		t.Error("missing X-Amz-Signature -- URL doesn't look presigned")
	}
}

func TestOpen_RejectsABadEndpoint(t *testing.T) {
	for _, endpoint := range []string{"", "minio:9000", "ftp://minio", "https://"} {
		if _, err := Open(Config{Endpoint: endpoint, Bucket: "b", AccessKeyID: "k", SecretAccessKey: "s"}); err == nil {
			t.Errorf("Open(endpoint %q): expected an error, got nil", endpoint)
		}
	}
}

// A self-hosted store has no wildcard DNS name for its buckets, so the
// bucket has to travel in the path, on the endpoint's own host and port.
func TestOpen_PathStyleKeepsTheEndpointHost(t *testing.T) {
	s, err := Open(Config{Endpoint: "http://minio.corp.example:9000", Bucket: "transfers", PathStyle: true, AccessKeyID: "k", SecretAccessKey: "s"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	rawURL, err := s.PresignGet(context.Background(), "exports/a.tar.zst", time.Minute, "")
	if err != nil {
		t.Fatalf("PresignGet: %v", err)
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("presigned URL %q did not parse: %v", rawURL, err)
	}
	if u.Scheme != "http" || u.Host != "minio.corp.example:9000" {
		t.Errorf("scheme://host = %s://%s, want http://minio.corp.example:9000", u.Scheme, u.Host)
	}
	if u.Path != "/transfers/exports/a.tar.zst" {
		t.Errorf("path = %q, want /transfers/exports/a.tar.zst", u.Path)
	}
}

// The region is part of what gets signed: a store that checks it rejects
// a link signed for any other. Empty keeps R2's "auto".
func TestOpen_RegionGoesIntoTheSignature(t *testing.T) {
	for region, want := range map[string]string{"": "auto", "us-east-1": "us-east-1"} {
		s, err := Open(Config{Endpoint: "https://s3.corp.example", Region: region, Bucket: "b", AccessKeyID: "k", SecretAccessKey: "s"})
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		rawURL, err := s.PresignGet(context.Background(), "k", time.Minute, "")
		if err != nil {
			t.Fatalf("PresignGet: %v", err)
		}
		u, _ := url.Parse(rawURL)
		if cred := u.Query().Get("X-Amz-Credential"); !strings.Contains(cred, "/"+want+"/s3/") {
			t.Errorf("region %q: credential scope = %q, want region %q in it", region, cred, want)
		}
	}
}
