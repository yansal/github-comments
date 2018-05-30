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
	app := newApp()
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

	g.Go(server(ctx, app.port, app.mux))
	g.Go(worker(ctx, app.receiver, "queue-fetch", app.fetcher.fetch))

	if err := g.Wait(); err != nil {
		log.Fatalf("%+v", err)
	}
}
