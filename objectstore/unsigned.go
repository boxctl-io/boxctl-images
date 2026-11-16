package objectstore

import (
	"context"

	"github.com/aws/smithy-go/middleware"
	smithyhttp "github.com/aws/smithy-go/transport/http"
)

// unsignedHeaders are request headers the SDK sets but leaves out of the
// SigV4 signature here. A proxy in front of the store may rewrite them:
// Cloudflare, for one, replaces Accept-Encoding, and a signature that
// covers it then fails with SignatureDoesNotMatch. The SDK signs
// "Accept-Encoding: identity" on every GetObject; S3 doesn't require it
// signed.
var unsignedHeaders = []string{"Accept-Encoding"}

type unsignedHeadersKey struct{}

// signWithoutHeaders takes unsignedHeaders off each request just before
// it is signed and puts them back just after, so they are still sent.
// Presigned URLs need nothing: they sign only the host.
func signWithoutHeaders(stack *middleware.Stack) error {
	if _, ok := stack.Finalize.Get("Signing"); !ok {
		return nil
	}
	if err := stack.Finalize.Insert(middleware.FinalizeMiddlewareFunc("UnsignedHeadersRemove",
		func(ctx context.Context, in middleware.FinalizeInput, next middleware.FinalizeHandler) (middleware.FinalizeOutput, middleware.Metadata, error) {
			if req, ok := in.Request.(*smithyhttp.Request); ok {
				saved := map[string][]string{}
				for _, h := range unsignedHeaders {
					if v := req.Header.Values(h); len(v) > 0 {
						saved[h] = v
						req.Header.Del(h)
					}
				}
				ctx = middleware.WithStackValue(ctx, unsignedHeadersKey{}, saved)
			}
			return next.HandleFinalize(ctx, in)
		}), "Signing", middleware.Before); err != nil {
		return err
	}
	return stack.Finalize.Insert(middleware.FinalizeMiddlewareFunc("UnsignedHeadersRestore",
		func(ctx context.Context, in middleware.FinalizeInput, next middleware.FinalizeHandler) (middleware.FinalizeOutput, middleware.Metadata, error) {
			if req, ok := in.Request.(*smithyhttp.Request); ok {
				saved, _ := middleware.GetStackValue(ctx, unsignedHeadersKey{}).(map[string][]string)
				for h, v := range saved {
					req.Header[h] = v
				}
			}
			return next.HandleFinalize(ctx, in)
		}), "Signing", middleware.After)
}
