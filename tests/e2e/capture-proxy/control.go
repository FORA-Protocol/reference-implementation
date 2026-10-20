package main

import (
	"encoding/json"
	"net/http"
	"strconv"
)

// controlHandler serves the harness's reads of the record, on a listener of its
// own so no path here can shadow a path of a party the proxy forwards to.
//
//   - GET /healthz answers 200 once the proxy runs;
//   - GET /captured answers the records past the sequence number "after",
//     narrowed to those whose request target, body, header values or decoded
//     message contain "contains". The answer also carries the oldest sequence
//     number still kept, so a reader can tell a window that is empty from one
//     whose records were evicted;
//   - GET /latest answers the latest sequence number taken, where a reader's
//     window begins.
func controlHandler(book *store) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /captured", func(w http.ResponseWriter, r *http.Request) {
		params := r.URL.Query()
		var after int64
		if raw := params.Get("after"); raw != "" {
			n, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || n < 0 {
				http.Error(w, "after must be a sequence number", http.StatusBadRequest)
				return
			}
			after = n
		}
		answer := book.read(query{after: after, contains: params.Get("contains")})
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(answer)
	})
	mux.HandleFunc("GET /latest", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]int64{"latest": book.latest()})
	})
	return mux
}
