package xrpcauth

import (
	"net/http"
	"strings"
)

func Route(native, api http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if api != nil && (r.URL.Path == "/xrpc" || strings.HasPrefix(r.URL.Path, "/xrpc/")) {
			api.ServeHTTP(w, r)
			return
		}
		native.ServeHTTP(w, r)
	})
}
