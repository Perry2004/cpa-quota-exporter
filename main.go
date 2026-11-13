package main

import (
	"flag"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:9108", "HTTP listen address")
	base := flag.String("cpa-url", "http://127.0.0.1:8317", "CLIProxyAPI base URL")
	keyFile := flag.String("management-key-file", "/var/lib/cliproxyapi/management-key", "file containing the management key")
	flag.Parse()
	key, err := os.ReadFile(*keyFile)
	if err != nil || strings.TrimSpace(string(key)) == "" {
		log.Fatal("cannot read a nonempty management key file")
	}
	e := newExporter(strings.TrimRight(*base, "/"), strings.TrimSpace(string(key)))
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", e)
	s := &http.Server{
		Addr:              *listen,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	log.Printf("serving /metrics on %s", *listen)
	log.Fatal(s.ListenAndServe())
}
