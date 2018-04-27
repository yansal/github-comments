package main

import (
	"log"
	"time"

	"github.com/lib/pq"
	"github.com/pkg/errors"
)

func newListener(databaseURL string, channels ...string) (*pq.Listener, error) {
	l := pq.NewListener(databaseURL, time.Second, time.Second, func(event pq.ListenerEventType, err error) {
		if err != nil {
			log.Printf("%+v", err)
		}
	})
	for _, channel := range channels {
		if err := l.Listen(channel); err != nil {
			return nil, errors.WithStack(err)
		}
	}
	return l, nil
}
