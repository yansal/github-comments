package main

import (
	"context"
	"os"
	"strconv"
	"text/template"

	"github.com/go-redis/redis"
	"github.com/google/go-github/github"
	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"
	"golang.org/x/oauth2"
	blackfriday "gopkg.in/russross/blackfriday.v2"
)

type config struct {
	store        *store
	cache        *cache
	broker       *broker
	receiver     *receiver
	fetcher      *fetcher
	port         string
	githubClient *github.Client
	template     *template.Template
}

func newConfig() *config {
	cfg := new(config)

	redisURL := os.Getenv("REDIS_URL")
	if redisURL == "" {
		redisURL = "redis://:6379"
	}
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		panic(err)
	}
	poolsize, _ := strconv.Atoi(os.Getenv("REDIS_POOL_SIZE"))
	opts.PoolSize = poolsize
	redis := redis.NewClient(opts)
	if err := redis.Ping().Err(); err != nil {
		panic(err)
	}
	cfg.cache = newCache(redis)
	cfg.broker = newBroker(redis)
	cfg.receiver = newReceiver(redis)

	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		databaseURL = "host=/tmp"
	}
	cfg.store = newStore(sqlx.MustConnect("postgres", databaseURL), cfg.cache)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	cfg.port = port

	githubToken := os.Getenv("GITHUB_TOKEN")
	if githubToken != "" {
		cfg.githubClient = github.NewClient(
			oauth2.NewClient(
				context.Background(),
				oauth2.StaticTokenSource(
					&oauth2.Token{
						AccessToken: githubToken,
					},
				),
			),
		)
	} else {
		cfg.githubClient = github.NewClient(nil)
	}

	cfg.fetcher = newFetcher(cfg)

	cfg.template = template.Must(template.New("").Funcs(template.FuncMap{
		"markdown": func(in string) string {
			return string(blackfriday.Run(
				[]byte(in),
				blackfriday.WithExtensions(blackfriday.CommonExtensions|blackfriday.HardLineBreak),
			))
		},
	}).ParseGlob("templates/*.html"))

	return cfg
}
