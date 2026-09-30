// A minimal service for the end-to-end test: it says which version and
// environment it is, so the scenario can tell what is deployed where.
package main

import (
	"fmt"
	"net/http"
	"os"
)

const version = "v1"

func main() {
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "hello %s from urbex (%s)\n", version, os.Getenv("GREETING_ENV"))
	})
	http.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	http.ListenAndServe(":"+os.Getenv("PORT"), nil)
}
