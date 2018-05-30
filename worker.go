package main

import "context"

func worker(ctx context.Context, receiver *receiver, queue string, f handler) func() error {
	return func() error {
		return receiver.Consume(ctx, queue, f)
	}
}
