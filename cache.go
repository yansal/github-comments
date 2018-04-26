package main

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/go-redis/redis"
	"github.com/pkg/errors"
)

type cache struct {
	redis *redis.Client
}

func newCache(redis *redis.Client) *cache { return &cache{redis: redis} }

func (cache *cache) LPush(key string, value interface{}) error {
	return cache.redis.LPush(key, value).Err()
}

func (cache *cache) LRange(key string, start, stop int64) ([]string, error) {
	ss, err := cache.redis.LRange("github-requests", start, stop).Result()
	if err == redis.Nil {
		return nil, nil
	}
	return ss, errors.WithStack(err)
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
	Message        string
	Timestamp      time.Time
	HTTPStatus     int
	Page, LastPage int
}

func (r *githubRequest) MarshalBinary() ([]byte, error) {
	return json.Marshal(r)
}
func (r *githubRequest) UnmarshalBinary(data []byte) error {
	return json.Unmarshal(data, r)
}
func (r githubRequest) String() string {
	s := fmt.Sprintf("%s: %d: %s", r.Timestamp.Format(time.RFC3339), r.HTTPStatus, r.Message)
	if r.HTTPStatus != 200 {
		return s
	}

	page := r.Page
	if page == 0 {
		page = 1
	}
	lastPage := r.LastPage
	if lastPage == 0 {
		lastPage = page
	}
	return fmt.Sprintf("%s (page %d/%d)", s, page, lastPage)
}
