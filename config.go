package main

import (
	"context"
	"os"
	"strconv"
	"text/template"
	"time"

	"github.com/go-redis/redis"
	"github.com/google/go-github/github"
	"github.com/jmoiron/sqlx"
	"github.com/pkg/errors"
	"golang.org/x/oauth2"
	blackfriday "gopkg.in/russross/blackfriday.v2"
)

type config struct {
	store        *store
	cache        *cache
	databaseURL  string
	port         string
	githubClient *github.Client
	template     *template.Template
}

func newConfig() *config {
	cfg := new(config)

	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		databaseURL = "host=/tmp"
	}
	cfg.databaseURL = databaseURL
	cfg.store = newStore(sqlx.MustConnect("postgres", cfg.databaseURL))

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

	cfg.template = template.Must(template.New("").Funcs(template.FuncMap{
		"markdown": func(in string) string {
			return string(blackfriday.Run(
				[]byte(in),
				blackfriday.WithExtensions(blackfriday.CommonExtensions|blackfriday.HardLineBreak),
			))
		},
		"until": func(t time.Time) string {
			return time.Until(t).Truncate(time.Second).String()
		},
		"repo": func(comment *github.IssueComment) (string, error) {
			url := comment.GetURL()
			match := commentURLRegexp.FindStringSubmatch(url)
			if len(match) < 4 {
				return "", errors.Errorf("couldn't match %s", url)
			}
			return match[1] + "/" + match[2], nil
		},
	}).ParseGlob("templates/*.html"))

	return cfg
}
