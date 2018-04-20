package main

import (
	"context"
	"log"
	"os"
	"time"

	"github.com/go-redis/redis"
	"github.com/google/go-github/github"
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
	"golang.org/x/oauth2"
)

type config struct {
	store        *store
	listener     *pq.Listener
	redis        *redis.Client
	port         string
	githubClient *github.Client
}

func newConfig() *config {
	cfg := new(config)

	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		databaseURL = "host=/tmp"
	}
	cfg.store = newStore(sqlx.MustConnect("postgres", databaseURL))

	cfg.listener = pq.NewListener(databaseURL, time.Second, time.Second, func(event pq.ListenerEventType, err error) {
		log.Println("listener callback:", event, err)
	})

	redisURL := os.Getenv("REDIS_URL")
	if redisURL == "" {
		redisURL = "redis://:6379"
	}
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		panic(err)
	}
	cfg.redis = redis.NewClient(opts)
	if err := cfg.redis.Ping().Err(); err != nil {
		panic(err)
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	cfg.port = port

	cfg.githubClient = github.NewClient(
		oauth2.NewClient(
			context.Background(),
			oauth2.StaticTokenSource(
				&oauth2.Token{
					AccessToken: os.Getenv("GITHUB_TOKEN"),
				},
			),
		),
	)

	return cfg
}
