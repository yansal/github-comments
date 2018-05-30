package main

import (
	"context"
	"log"
	"time"

	"github.com/go-redis/redis"
	"github.com/pkg/errors"
)

type receiver struct{ redis *redis.Client }

func newReceiver(redis *redis.Client) *receiver { return &receiver{redis: redis} }

type handler func(context.Context, []byte) error

func (r *receiver) Consume(ctx context.Context, queue string, f handler) error {
	cerr := make(chan error) // for consumeLoop to notify Consume when it shutdowns
	go r.consumeLoop(ctx, queue, f, cerr)

	select {
	case <-ctx.Done():
		<-cerr // wait for consumeLoop to shutdown
		return ctx.Err()
	case err := <-cerr:
		return err
	}
}

func (r *receiver) consumeLoop(ctx context.Context, queue string, f handler, cerr chan error) {
	processing := queue + "-processing"
	// TODO: republish message in processing back to queue?

	type msg struct {
		bytes []byte
		err   error
	}
	brpoplpush := make(chan msg)

	for {
		go func() {
			// TODO: acquire lock? see https://stackoverflow.com/a/34754632
			bytes, err := r.redis.BRPopLPush(queue, processing, time.Minute).Bytes()
			brpoplpush <- msg{bytes: bytes, err: err}
		}()

		var payload []byte
		select {
		case <-ctx.Done():
			cerr <- nil
			return
		case msg := <-brpoplpush:
			if err := msg.err; err == redis.Nil {
				continue
			} else if err != nil {
				cerr <- errors.WithStack(err)
				return
			}
			payload = msg.bytes
		}

		if err := f(ctx, payload); errors.Cause(err) == context.Canceled {
			cerr <- nil
		} else if err != nil {
			log.Printf("%+v\n", err)
			continue
		}

		if err := r.redis.LRem(processing, 0, payload).Err(); err != nil {
			cerr <- errors.WithStack(err)
			return
		}
	}
}
