package main

import (
	"context"
	"log"
	"time"

	"github.com/lib/pq"
	"github.com/pkg/errors"
)

func listener(ctx context.Context, cfg *config, f func(context.Context, *config), channels ...string) func() error {
	return func() error {
		l := pq.NewListener(cfg.databaseURL, time.Second, time.Second, func(event pq.ListenerEventType, err error) {
			if err != nil {
				log.Printf("%+v", err)
			}
		})
		defer l.Close()

		for _, channel := range channels {
			if err := l.Listen(channel); err != nil {
				return errors.WithStack(err)
			}
		}

		for {
			f(ctx, cfg)
			select {
			case <-l.Notify:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
}
