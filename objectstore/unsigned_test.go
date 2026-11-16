package objectstore

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A proxy that rewrites Accept-Encoding (Cloudflare does) must not break
// the signature, so the header is sent but not signed.
func TestGet_AcceptEncodingIsSentButNotSigned(t *testing.T) {
	var auth, enc string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth, enc = r.Header.Get("Authorization"), r.Header.Get("Accept-Encoding")
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	s, err := Open(Config{Endpoint: srv.URL, Bucket: "b", PathStyle: true, AccessKeyID: "k", SecretAccessKey: "s"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(context.Background(), "x"); err != nil {
		t.Fatal(err)
	}
	if enc != "identity" {
		t.Errorf("Accept-Encoding = %q, want identity (still sent)", enc)
	}
	_, signed, _ := strings.Cut(auth, "SignedHeaders=")
	signed, _, _ = strings.Cut(signed, ",")
	if signed == "" || strings.Contains(signed, "accept-encoding") {
		t.Errorf("SignedHeaders = %q, want it without accept-encoding", signed)
	}
}
