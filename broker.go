package main

import (
	"github.com/go-redis/redis"
	"github.com/pkg/errors"
)

type broker struct{ redis *redis.Client }

func newBroker(redis *redis.Client) *broker { return &broker{redis: redis} }

func (b *broker) Publish(queue string, value interface{}) error {
	length, err := b.redis.LPush(queue, value).Result()
	if err != nil {
		return errors.WithStack(err)
	}

	if err := b.redis.Set(queue+"-count", length, 0).Err(); err != nil {
		return errors.WithStack(err)
	}
	return b.redis.Publish(queue+"-count", length).Err()
}
