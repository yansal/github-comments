package main

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"
	"text/template"
	"time"

	"github.com/google/go-github/github"
	"github.com/gorilla/websocket"
	"github.com/pkg/errors"
)

func newMux(broker *broker, cache *cache, store *store, template *template.Template) *http.ServeMux {
	mux := http.NewServeMux()
	mux.Handle("/favicon.ico", http.NotFoundHandler())
	mux.Handle("/_status", statusHandler(cache, template))
	mux.Handle("/_ws", wsHandler(cache))
	mux.Handle("/", rootHandler(broker, store, template))
	return mux
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

func statusHandler(cache *cache, template *template.Template) http.HandlerFunc {
	return handleError(func(w http.ResponseWriter, r *http.Request) error {
		var data struct {
			IssueCount, RepoCount, UserCount int
			CoreRate, SearchRate             *github.Rate
			Requests                         []githubRequest
		}

		b, err := cache.Get("count-issue")
		if err != nil {
			log.Print(err)
		} else if b != nil {
			data.IssueCount, _ = strconv.Atoi(string(b))
		}

		b, err = cache.Get("count-repo")
		if err != nil {
			log.Print(err)
		} else if b != nil {
			data.RepoCount, _ = strconv.Atoi(string(b))
		}

		b, err = cache.Get("count-user")
		if err != nil {
			log.Print(err)
		} else if b != nil {
			data.UserCount, _ = strconv.Atoi(string(b))
		}

		b, err = cache.Get("github-search-rate")
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
		b, err = cache.Get("github-core-rate")
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

		ss, err := cache.LRange("github-requests", 0, 1000)
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

		return errors.WithStack(
			template.ExecuteTemplate(w, "status.html", data))
	})
}

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
}

func wsHandler(cache *cache) http.HandlerFunc {
	return handleError(func(w http.ResponseWriter, r *http.Request) error {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return errors.WithStack(err)
		}
		defer conn.Close()

		wsc := make(chan struct{})
		go func() {
			for {
				_, _, err := conn.ReadMessage()
				if _, ok := err.(*websocket.CloseError); ok {
					close(wsc)
					return
				} else if err != nil {
					log.Printf("%+v\n", err)
				}
			}
		}()

		// TODO: abstract redis pubsub in cache
		pubsub := cache.redis.PSubscribe("github-requests", "github-*-rate", "count-*")
		defer pubsub.Close()

		pubsubc := pubsub.Channel()
		for {
			select {
			case <-wsc: // websocket is closed
				return nil
			case msg := <-pubsubc:
				wsMsg := struct {
					Channel string      `json:"channel"`
					Pattern string      `json:"pattern"`
					Payload interface{} `json:"payload"`
				}{Channel: msg.Channel, Pattern: msg.Pattern}

				switch msg.Pattern {
				case "github-requests":
					var r githubRequest
					if err := json.Unmarshal([]byte(msg.Payload), &r); err != nil {
						return errors.WithStack(err)
					}
					wsMsg.Payload = r.String()
				case "github-*-rate":
					var rate github.Rate
					if err := json.Unmarshal([]byte(msg.Payload), &rate); err != nil {
						return errors.WithStack(err)
					}
					wsMsg.Payload = rate
				case "count-*":
					wsMsg.Payload = msg.Payload
				}

				if err := conn.WriteJSON(wsMsg); err != nil {
					return errors.WithStack(err)
				}
			}
		}
	})
}

func rootHandler(broker *broker, store *store, template *template.Template) http.HandlerFunc {
	return handleError(func(w http.ResponseWriter, r *http.Request) error {
		start := time.Now()
		var (
			comments []comment
			err      error
		)

		split := strings.Split(r.URL.Path, "/")
		ctx := r.Context()
		switch {
		case len(split) >= 3 && split[2] != "":
			owner := split[1]
			repo := split[2]
			comments, err = store.getCommentsForRepo(ctx, owner, repo)
			if err != nil {
				return err
			}

			if err := broker.Publish("queue-fetch", repoPayload{Owner: owner, Name: repo}); err != nil {
				return err
			}
		case len(split) >= 2 && split[1] != "":
			user := split[1]
			comments, err = store.getCommentsForUser(ctx, user)
			if err != nil {
				return err
			}
			if err := broker.Publish("queue-fetch", userPayload{Login: user}); err != nil {
				return err
			}
		default:
			comments, err = store.getComments(ctx)
			if err != nil {
				return err
			}
		}

		data := struct {
			Duration time.Duration
			Comments []comment
		}{Comments: comments, Duration: time.Since(start)}

		return errors.WithStack(
			template.ExecuteTemplate(w, "index.html", data))
	})
}
