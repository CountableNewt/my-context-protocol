package xrpc

import (
	"errors"
	"net/http"
	"net/url"
)

func parseQuery(r *http.Request) (map[string]string, error) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return nil, errors.New("Malformed query parameters")
	}
	out := make(map[string]string, len(query))
	for name, values := range query {
		if len(values) != 1 {
			return nil, errors.New("Query parameters must occur exactly once")
		}
		out[name] = values[0]
	}
	return out, nil
}
