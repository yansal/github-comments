package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/google/go-github/github"
	"github.com/gorilla/websocket"
	"github.com/pkg/errors"
)

func newServer(ctx context.Context, cfg *config) *server {
	mux := http.NewServeMux()
	mux.Handle("/favicon.ico", http.NotFoundHandler())
	mux.Handle("/_status", statusHandler(cfg))
	mux.Handle("/_ws", wsHandler(cfg))
	mux.Handle("/", rootHandler(cfg))

	return &server{
		ctx: ctx,
		server: http.Server{
			Addr:    ":" + cfg.port,
			Handler: mux,
		},
	}
}

type server struct {
	ctx    context.Context
	server http.Server
}

func (s *server) run() error {
	cerr := make(chan error)
	go func() { cerr <- s.server.ListenAndServe() }()
	select {
	case err := <-cerr:
		return err
	case <-s.ctx.Done():
		return s.server.Shutdown(context.Background())
	}
}

type handlerFunc func(w http.ResponseWriter, r *http.Request) error

func handleError(h handlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := h(w, r); err != nil {
			log.Printf("%+v", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}
}

func statusHandler(cfg *config) http.HandlerFunc {
	return handleError(func(w http.ResponseWriter, r *http.Request) error {
		start := time.Now()

		var data struct {
			Duration   time.Duration
			JobCounts  []jobCount
			SearchRate *github.Rate
			CoreRate   *github.Rate
			Requests   []githubRequest
		}

		ctx := r.Context()
		jobCounts, err := cfg.store.countJobs(ctx)
		if err != nil {
			return err
		}
		data.JobCounts = jobCounts

		b, err := cfg.cache.Get("github-search-rate")
		if err != nil {
			log.Print(err)
		} else if b != nil {
			var searchRate github.Rate
			if err := json.Unmarshal(b, &searchRate); err != nil {
				log.Print(err)
			} else {
				data.SearchRate = &searchRate
			}
		}
		b, err = cfg.cache.Get("github-core-rate")
		if err != nil {
			log.Print(err)
		} else if b != nil {
			var coreRate github.Rate
			if err := json.Unmarshal(b, &coreRate); err != nil {
				log.Print(err)
			} else {
				data.CoreRate = &coreRate
			}
		}

		ss, err := cfg.cache.LRange("github-requests", 0, 1000)
		if err != nil {
			log.Print(err)
		}
		for i := range ss {
			var r githubRequest
			if err := json.Unmarshal([]byte(ss[i]), &r); err != nil {
				log.Print(err)
			}
			data.Requests = append(data.Requests, r)
		}

		data.Duration = time.Since(start)
		return errors.WithStack(
			cfg.template.ExecuteTemplate(w, "status.html", data))
	})
}

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
}

func wsHandler(cfg *config) http.HandlerFunc {
	return handleError(func(w http.ResponseWriter, r *http.Request) error {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return errors.WithStack(err)
		}
		defer conn.Close()

		// TODO: abstract redis pubsub in cache
		pubsub := cfg.cache.redis.Subscribe("github-requests")
		defer pubsub.Close()
		for msg := range pubsub.Channel() {
			var r githubRequest
			if err := json.Unmarshal([]byte(msg.Payload), &r); err != nil {
				return errors.WithStack(err)
			}
			if err := conn.WriteMessage(websocket.TextMessage, []byte(r.String())); err != nil {
				return errors.WithStack(err)
			}
		}
		return nil
	})
}

func rootHandler(cfg *config) http.HandlerFunc {
	return handleError(func(w http.ResponseWriter, r *http.Request) error {
		start := time.Now()
		var (
			comments []github.IssueComment
			err      error
		)

		split := strings.Split(r.URL.Path, "/")
		ctx := r.Context()
		switch {
		case len(split) >= 3 && split[2] != "":
			owner := split[1]
			repo := split[2]
			comments, err = cfg.store.getCommentsForRepo(ctx, owner, repo)
			if err != nil {
				return err
			}

			if err := cfg.store.insertJobRepo(ctx, jobRepo{Owner: owner, Name: repo}); err != nil {
				return err
			}
		case len(split) >= 2 && split[1] != "":
			user := split[1]
			comments, err = cfg.store.getCommentsForUser(ctx, user)
			if err != nil {
				return err
			}
			if err := cfg.store.insertJobUser(ctx, jobUser{Login: user}); err != nil {
				return err
			}
		default:
			comments, err = cfg.store.getComments(ctx)
			if err != nil {
				return err
			}
		}

		data := struct {
			Duration time.Duration
			Comments []github.IssueComment
		}{Comments: comments, Duration: time.Since(start)}

		return errors.WithStack(
			cfg.template.ExecuteTemplate(w, "index.html", data))
	})
}
