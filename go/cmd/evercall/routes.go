package main

import (
	"fmt"
	"net/http"

	"github.com/nathejk/evercall/internal/vcs"
)

// routes deliberately registers a single catch-all pattern: the point of this
// stage is to capture whatever the provider sends, on whatever path and method
// it chooses, rather than to reject anything we did not anticipate.
//
// "/" in net/http's mux matches every path and every method. Once the real
// webhook contract is known, this grows into explicit routes (see the
// go-service-layout skill) — it does not stay a wildcard.
func (app *application) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthcheck", app.healthcheckHandler)
	mux.HandleFunc("/", app.dumpHandler)

	return mux
}

func (app *application) healthcheckHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprintf(w, "{\"status\":\"available\",\"version\":%q,\"commit\":%q}\n", vcs.Version, vcs.Commit)
}
