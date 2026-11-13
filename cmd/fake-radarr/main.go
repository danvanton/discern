// Command fake-radarr runs the in-memory stand-in for Radarr
// (internal/radarr/radarrtest) on a port, so Discern can be run and clicked
// through on a development machine without a real Radarr.
//
//	fake-radarr -listen :7878 -staging ./dev/staging -library ./dev/library
//
// Its API key is "dev-key". Every movie lookup returns the same few films.
package main

import (
	"flag"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/danvanton/discern/internal/radarr/radarrtest"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:7878", "address to listen on")
	staging := flag.String("staging", "dev/staging", "Discern's staging folder")
	remote := flag.String("remote", "", "the staging folder as Radarr would see it (default: same as -staging)")
	library := flag.String("library", "dev/library", "where imported files go")
	flag.Parse()

	for _, d := range []string{*staging, *library} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			log.Fatal(err)
		}
	}
	local, _ := filepath.Abs(*staging)
	lib, _ := filepath.Abs(*library)
	if *remote == "" {
		*remote = filepath.ToSlash(local)
	}
	s := radarrtest.New("dev-key", local, *remote, lib)
	s.SetLookup(
		map[string]any{"tmdbId": 603, "title": "The Matrix", "year": 1999},
		map[string]any{"tmdbId": 559, "title": "Spider-Man 3", "year": 2007},
		map[string]any{"tmdbId": 335984, "title": "Blade Runner 2049", "year": 2017},
	)
	log.Printf("fake Radarr on http://%s (API key dev-key), staging %s, library %s", *listen, local, lib)
	srv := &http.Server{Addr: *listen, Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
	log.Fatal(srv.ListenAndServe())
}
