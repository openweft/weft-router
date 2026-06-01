// Package metrics exposes a Prometheus /metrics endpoint on a port that's
// separate from the BGP listener, so the scrape surface doesn't share
// fate with the data plane (a metrics handler hang can't stall route
// updates).
package metrics

import (
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// NewServer returns an *http.Server that serves /metrics at addr. Caller
// owns the goroutine that calls ListenAndServe.
//
// We use a fresh ServeMux (instead of http.DefaultServeMux) so the metrics
// handler can't accidentally pick up debug routes some other lib might
// register on the default mux.
func NewServer(addr string) *http.Server {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	return &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
}
