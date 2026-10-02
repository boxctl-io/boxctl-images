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
