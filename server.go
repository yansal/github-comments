package main

import (
	"context"
	"net/http"
)

func server(ctx context.Context, port string, mux *http.ServeMux) func() error {
	return func() error {
		s := http.Server{
			Addr:    ":" + port,
			Handler: mux,
		}

		cerr := make(chan error)
		go func() { cerr <- s.ListenAndServe() }()
		select {
		case err := <-cerr:
			return err
		case <-ctx.Done():
			return s.Shutdown(context.Background())
		}
	}
}
