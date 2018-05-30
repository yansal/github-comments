package main

import (
	"context"
	"net/http"
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

type app struct {
	receiver *receiver
	fetcher  *fetcher

	port string
	mux  *http.ServeMux
}

func newApp() *app {
	app := new(app)

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
	cache := newCache(redis)
	broker := newBroker(redis)

	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		databaseURL = "host=/tmp"
	}
	store := newStore(sqlx.MustConnect("postgres", databaseURL))

	var githubClient *github.Client
	githubToken := os.Getenv("GITHUB_TOKEN")
	if githubToken != "" {
		githubClient = github.NewClient(
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
		githubClient = github.NewClient(nil)
	}

	app.receiver = newReceiver(redis)
	app.fetcher = newFetcher(broker, cache, store, githubClient)

	template := template.Must(template.New("").Funcs(template.FuncMap{
		"markdown": func(in string) string {
			return string(blackfriday.Run(
				[]byte(in),
				blackfriday.WithExtensions(blackfriday.CommonExtensions|blackfriday.HardLineBreak),
			))
		},
	}).ParseGlob("templates/*.html"))

	app.port = os.Getenv("PORT")
	if app.port == "" {
		app.port = "8080"
	}
	app.mux = newMux(broker, cache, store, template)

	return app
}
