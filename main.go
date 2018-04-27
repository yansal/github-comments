package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"golang.org/x/sync/errgroup"
)

func main() {
	log.SetFlags(log.Lshortfile)

	cfg := newConfig()
	g, ctx := errgroup.WithContext(context.Background())

	// Signal handler
	g.Go(func() error {
		c := make(chan os.Signal, 1)
		signal.Notify(c, os.Interrupt, syscall.SIGTERM)
		select {
		case s := <-c:
			return fmt.Errorf("%v", s)
		case <-ctx.Done():
			return nil
		}
	})

	// HTTP server
	g.Go(server(ctx, cfg))

	// GitHub fetchers
	for i := 0; i < 2; i++ {
		g.Go(listener(ctx, cfg, fetch, "fetcher"))
	}

	if err := g.Wait(); err != nil {
		log.Fatalf("%+v", err)
	}
}
