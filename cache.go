package main

import (
	"time"

	"github.com/go-redis/redis"
	"github.com/google/go-github/github"
	"github.com/pkg/errors"
)

type cache struct{ redis *redis.Client }

func newCache(redis *redis.Client) *cache { return &cache{redis: redis} }

func (c *cache) Incr(key string) (int64, error) {
	val, err := c.redis.Incr(key).Result()
	return val, errors.WithStack(err)
}
func (c *cache) IncrBy(key string, value int64) (int64, error) {
	val, err := c.redis.IncrBy(key, value).Result()
	return val, errors.WithStack(err)
}

func (c *cache) LPush(key string, value interface{}) error {
	return errors.WithStack(c.redis.LPush(key, value).Err())
}

func (c *cache) LTrim(key string, start, stop int64) error {
	return errors.WithStack(c.redis.LTrim(key, start, stop).Err())
}

func (c *cache) LRange(key string, start, stop int64) ([]string, error) {
	ss, err := c.redis.LRange(key, start, stop).Result()
	if err == redis.Nil {
		return nil, nil
	}
	return ss, errors.WithStack(err)
}

func (c *cache) Publish(channel string, message interface{}) error {
	return errors.WithStack(c.redis.Publish(channel, message).Err())
}

func (c *cache) Get(key string) ([]byte, error) {
	b, err := c.redis.Get(key).Bytes()
	if err == redis.Nil {
		return nil, nil
	}
	return b, errors.WithStack(err)
}

func (c *cache) Set(key string, value interface{}, expiration time.Duration) error {
	return errors.WithStack(c.redis.Set(key, value, expiration).Err())
}

func (c *cache) sendToRequestLog(message string, opts github.ListOptions, resp *github.Response, duration time.Duration) error {
	now := time.Now()
	incr, err := c.Incr("github-requests-id")
	if err != nil {
		return err
	}
	r := &githubRequest{
		ID:          incr,
		Timestamp:   now,
		Message:     message,
		ListOptions: opts,
		StatusCode:  resp.StatusCode,
		LastPage:    resp.LastPage,
		Duration:    duration,
	}
	if err := c.LPush("github-requests", r); err != nil {
		return err
	}
	if err := c.LTrim("github-requests", 0, 1000); err != nil {
		return err
	}
	return c.Publish("github-requests", r)
}

func (c *cache) updateRate(key string, rate github.Rate) error {
	if err := c.Set(key, githubRate(rate), time.Until(rate.Reset.Time)); err != nil {
		return err

	}
	return c.Publish(key, githubRate(rate))
}
