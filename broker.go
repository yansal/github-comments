package main

import (
	"github.com/go-redis/redis"
	"github.com/pkg/errors"
)

type broker struct{ redis *redis.Client }

func newBroker(redis *redis.Client) *broker { return &broker{redis: redis} }

func (b *broker) Publish(key string, value interface{}) error {
	return errors.WithStack(b.redis.LPush(key, value).Err())
}
