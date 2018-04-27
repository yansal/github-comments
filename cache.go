package main

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/go-redis/redis"
	"github.com/google/go-github/github"
	"github.com/pkg/errors"
)

type cache struct {
	redis *redis.Client
}

func newCache(redis *redis.Client) *cache { return &cache{redis: redis} }

func (cache *cache) LPush(key string, value interface{}) error {
	return cache.redis.LPush(key, value).Err()
}

func (cache *cache) LTrim(key string, start, stop int64) error {
	return cache.redis.LTrim(key, start, stop).Err()
}

func (cache *cache) LRange(key string, start, stop int64) ([]string, error) {
	ss, err := cache.redis.LRange(key, start, stop).Result()
	if err == redis.Nil {
		return nil, nil
	}
	return ss, errors.WithStack(err)
}

func (cache *cache) Publish(channel string, message interface{}) error {
	return errors.WithStack(cache.redis.Publish(channel, message).Err())
}

func (cache *cache) Get(key string) ([]byte, error) {
	b, err := cache.redis.Get(key).Bytes()
	if err == redis.Nil {
		return nil, nil
	}
	return b, errors.WithStack(err)
}

func (cache *cache) Set(key string, value interface{}, expiration time.Duration) error {
	return cache.redis.Set(key, value, expiration).Err()
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

func (cache *cache) sendToRequestLog(message string, opts github.ListOptions, resp *github.Response, duration time.Duration) error {
	now := time.Now()
	incr, err := cache.redis.Incr("github-requests-id").Result()
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
	if err := cache.LPush("github-requests", r); err != nil {
		return err
	}
	if err := cache.LTrim("github-requests", 0, 1000); err != nil {
		return err
	}
	return cache.Publish("github-requests", r)
}
