package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"text/template"
	"time"

	"github.com/go-redis/redis"
	"github.com/google/go-github/github"
	"github.com/pkg/errors"
	blackfriday "gopkg.in/russross/blackfriday.v2"
)

func newServer(ctx context.Context, cfg *config) *server {
	return &server{
		ctx: ctx,
		server: http.Server{
			Addr: ":" + cfg.port,
			Handler: &handler{
				store: cfg.store,
				redis: cfg.redis,
				template: template.Must(template.New("").Funcs(template.FuncMap{
					"markdown": func(in string) string {
						return string(blackfriday.Run(
							[]byte(in),
							blackfriday.WithExtensions(blackfriday.CommonExtensions|blackfriday.HardLineBreak),
						))
					},
					"until": func(t time.Time) string {
						return time.Until(t).Truncate(time.Second).String()
					},
				}).ParseFiles("index.html")),
			},
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

type handler struct {
	store    *store
	redis    *redis.Client
	template *template.Template
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if err := h.serve(w, r); err != nil {
		log.Printf("%+v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (h *handler) serve(w http.ResponseWriter, r *http.Request) error {
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
		comments, err = h.store.getCommentsForRepo(ctx, owner, repo)
		if err != nil {
			return err
		}
		if err := h.store.insertPayload(ctx, strings.Join(split[1:], "/")); err != nil {
			return err
		}
	case len(split) >= 2 && split[1] != "":
		user := split[1]
		comments, err = h.store.getCommentsForUser(ctx, user)
		if err != nil {
			return err
		}
		if err := h.store.insertPayload(ctx, user); err != nil {
			return err
		}
	default:
		comments, err = h.store.getComments(ctx)
		if err != nil {
			return err
		}
	}

	data := struct {
		Comments   []github.IssueComment
		SearchRate *github.Rate
		CoreRate   *github.Rate
		Duration   time.Duration
	}{Comments: comments}

	b, err := h.redis.Get("github-search-rate").Bytes()
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
	b, err = h.redis.Get("github-core-rate").Bytes()
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
	data.Duration = time.Since(start)
	return errors.WithStack(
		h.template.ExecuteTemplate(w, "index.html", data))
}
