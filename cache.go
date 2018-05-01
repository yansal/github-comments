package main

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/go-redis/redis"
	"github.com/google/go-github/github"
	"github.com/pkg/errors"
)

type cache struct{ redis *redis.Client }

func newCache(redis *redis.Client) *cache { return &cache{redis: redis} }

func (c *cache) Incr(key string) (int64, error) {
	return c.redis.Incr(key).Result()
}
func (c *cache) IncrBy(key string, value int64) (int64, error) {
	return c.redis.IncrBy(key, value).Result()
}

func (c *cache) LPush(key string, value interface{}) error {
	return c.redis.LPush(key, value).Err()
}

func (c *cache) LTrim(key string, start, stop int64) error {
	return c.redis.LTrim(key, start, stop).Err()
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

type githubRequest struct {
	ID          int64
	Timestamp   time.Time
	Message     string
	ListOptions github.ListOptions
	StatusCode  int
	LastPage    int
	Duration    time.Duration
}

func (r *githubRequest) MarshalBinary() ([]byte, error) {
	return json.Marshal(r)
}

func (r *githubRequest) UnmarshalBinary(data []byte) error {
	return json.Unmarshal(data, r)
}

func (r githubRequest) String() string {
	message := r.Message
	if r.StatusCode == 200 {
		page := r.ListOptions.Page
		if page == 0 {
			page = 1
		}
		lastPage := r.LastPage
		if lastPage == 0 {
			lastPage = page
		}
		message = fmt.Sprintf("%s (%d/%d)", message, page, lastPage)
	}
	return fmt.Sprintf("ts=%s msg=%q status=%d duration=%s", r.Timestamp.Format(time.RFC3339), message, r.StatusCode, r.Duration)
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

type githubRate github.Rate

func (r githubRate) MarshalBinary() ([]byte, error) {
	return json.Marshal(r)
}

func (r githubRate) UnmarshalBinary(data []byte) error {
	return json.Unmarshal(data, r)
}

func (c *cache) updateRate(key string, rate github.Rate) error {
	if err := c.Set(key, githubRate(rate), time.Until(rate.Reset.Time)); err != nil {
		return err

	}
	return c.Publish(key, githubRate(rate))
}

func (c *cache) updateCount(fetchItemType string, incr int64) error {
	key := "count-" + fetchItemType
	count, err := c.IncrBy(key, incr)
	if err != nil {
		return errors.WithStack(err)
	}
	return c.Publish(key, count)
}
