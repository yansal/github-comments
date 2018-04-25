package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/go-redis/redis"
	"github.com/google/go-github/github"
	"github.com/pkg/errors"
)

func newServer(ctx context.Context, cfg *config) *server {
	mux := http.NewServeMux()
	mux.Handle("/favicon.ico", http.NotFoundHandler())
	mux.Handle("/_status", statusHandler(cfg))
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

type githubRequest struct {
	Timestamp      time.Time
	HTTPStatus     int
	Page, LastPage int
	Description    string
}

func (r *githubRequest) MarshalBinary() ([]byte, error) {
	return json.Marshal(r)
}
func (r *githubRequest) UnmarshalBinary(data []byte) error {
	return json.Unmarshal(data, r)
}
func (r githubRequest) String() string {
	return fmt.Sprintf("%s: %d: %s: page %d/%d", r.Timestamp.Format(time.RFC3339), r.HTTPStatus, r.Description, r.Page, r.LastPage)
}

func statusHandler(cfg *config) http.HandlerFunc {
	return handleError(func(w http.ResponseWriter, r *http.Request) error {
		start := time.Now()

		var data struct {
			SearchRate *github.Rate
			CoreRate   *github.Rate
			Duration   time.Duration
			Requests   []githubRequest
		}

		b, err := cfg.redis.Get("github-search-rate").Bytes()
		if err == redis.Nil {
		} else if err != nil {
			log.Print(err)
		} else {
			var searchRate github.Rate
			if err := json.Unmarshal(b, &searchRate); err != nil {
				log.Print(err)
			} else {
				data.SearchRate = &searchRate
			}
		}
		b, err = cfg.redis.Get("github-core-rate").Bytes()
		if err == redis.Nil {
		} else if err != nil {
			log.Print(err)
		} else {
			var coreRate github.Rate
			if err := json.Unmarshal(b, &coreRate); err != nil {
				log.Print(err)
			} else {
				data.CoreRate = &coreRate
			}
		}

		ss, err := cfg.redis.LRange("github-requests", 0, 100).Result()
		if err == redis.Nil {
		} else if err != nil {
			log.Print(err)
		} else {
			for i := range ss {
				var r githubRequest
				if err := json.Unmarshal([]byte(ss[i]), &r); err != nil {
					log.Print(err)
				}
				data.Requests = append(data.Requests, r)
			}
		}
		data.Duration = time.Since(start)
		return errors.WithStack(
			cfg.template.ExecuteTemplate(w, "status.html", data))
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
			if err := cfg.store.insertPayload(ctx, strings.Join(split[1:], "/")); err != nil {
				return err
			}
		case len(split) >= 2 && split[1] != "":
			user := split[1]
			comments, err = cfg.store.getCommentsForUser(ctx, user)
			if err != nil {
				return err
			}
			if err := cfg.store.insertPayload(ctx, user); err != nil {
				return err
			}
		default:
			comments, err = cfg.store.getComments(ctx)
			if err != nil {
				return err
			}
		}

		data := struct {
			Comments []github.IssueComment
			Duration time.Duration
		}{Comments: comments, Duration: time.Since(start)}

		return errors.WithStack(
			cfg.template.ExecuteTemplate(w, "index.html", data))
	})
}
