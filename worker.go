package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/go-github/github"
	"github.com/lib/pq"
	"github.com/pkg/errors"
	"golang.org/x/sync/errgroup"
)

func newWorker(ctx context.Context, cfg *config) *worker {
	return &worker{
		ctx:          ctx,
		store:        cfg.store,
		listener:     cfg.listener,
		cache:        cfg.cache,
		githubClient: cfg.githubClient,
	}
}

type worker struct {
	ctx          context.Context
	store        *store
	listener     *pq.Listener
	cache        *cache
	githubClient *github.Client
}

func (w *worker) run() error {
	if err := w.listener.Listen("users"); err != nil {
		return err
	}

	g, ctx := errgroup.WithContext(w.ctx)
	for i := 0; i < 2; i++ {
		g.Go(func() error {
			for {
				if err := w.work(ctx); err != nil {
					return err
				}
				select {
				case <-w.listener.Notify:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
		})
	}
	return g.Wait()
}

func (w *worker) work(ctx context.Context) error {
	for {
		// TODO: abstract db transaction in store
		tx, err := w.store.BeginTxx(ctx, nil)
		if err != nil {
			return errors.WithStack(err)
		}
		defer func() {
			if err := tx.Rollback(); err != sql.ErrTxDone && err != nil {
				log.Printf("%+v", errors.WithStack(err))
			}
		}()

		var payload string
		if err := tx.Get(&payload, `select login from users order by created_at limit 1 for update skip locked`); err == sql.ErrNoRows {
			return nil
		} else if err != nil {
			return errors.WithStack(err)
		}

		split := strings.Split(payload, "/")
		switch len(split) {
		case 1:
			if err := w.searchIssues(ctx, payload); err != nil {
				return err
			}
		case 2:
			if err := w.listIssues(ctx, split[0], split[1]); err != nil {
				return err
			}
		default:
			log.Printf("don't know what to do with payload %s", payload)
			return nil
		}
		if _, err := tx.Exec(`delete from users where login = $1`, payload); err != nil {
			return errors.WithStack(err)
		}
		if err := tx.Commit(); err != nil {
			return errors.WithStack(err)
		}
	}
}

func (w *worker) searchIssues(ctx context.Context, user string) error {
	query := fmt.Sprintf(`commenter:"%s"`, user)
	opts := &github.SearchOptions{Sort: "updated", Order: "desc", ListOptions: github.ListOptions{PerPage: 100}}
	for {
		result, resp, err := w.githubClient.Search.Issues(ctx, query, opts)
		if resp != nil {
			b, _ := json.Marshal(resp.Rate)
			if err := w.cache.Set("github-search-rate", b, time.Until(resp.Reset.Time)); err != nil {
				log.Print(err)
			}
			if err := w.cache.LPush("github-requests", &githubRequest{
				Timestamp: time.Now(), HTTPStatus: resp.StatusCode, Page: opts.Page, LastPage: resp.LastPage, Description: fmt.Sprintf("search issues commented by %s", user),
			}); err != nil {
				log.Print(err)
			}
		}
		if _, ok := err.(*github.RateLimitError); ok {
			select {
			case <-time.After(time.Until(resp.Reset.Time)):
				continue
			case <-ctx.Done():
				return ctx.Err()
			}
		} else if gerr, ok := err.(*github.ErrorResponse); ok {
			log.Print(gerr)
			return nil
		} else if err != nil {
			return errors.WithStack(err)
		}

		for i := range result.Issues {
			if err := w.listComments(ctx, &result.Issues[i]); err != nil {
				return err
			}

			if err := w.store.insertIssue(ctx, &result.Issues[i]); err != nil {
				return err
			}
		}

		if resp.NextPage <= opts.ListOptions.Page {
			break
		}
		opts.ListOptions.Page = resp.NextPage
	}
	return nil
}

func (w *worker) listIssues(ctx context.Context, owner, repo string) error {
	// TODO: order by reactions?
	opts := &github.IssueListByRepoOptions{Sort: "updated", State: "all", ListOptions: github.ListOptions{PerPage: 100}}
	for {
		issues, resp, err := w.githubClient.Issues.ListByRepo(ctx, owner, repo, opts)
		if resp != nil {
			b, _ := json.Marshal(resp.Rate)
			if err := w.cache.Set("github-core-rate", b, time.Until(resp.Reset.Time)); err != nil {
				log.Print(err)
			}
			if err := w.cache.LPush("github-requests", &githubRequest{
				Timestamp: time.Now(), HTTPStatus: resp.StatusCode, Page: opts.Page, LastPage: resp.LastPage, Description: fmt.Sprintf("list %s/%s issues", owner, repo),
			}); err != nil {
				log.Print(err)
			}
		}
		if _, ok := err.(*github.RateLimitError); ok {
			select {
			case <-time.After(time.Until(resp.Reset.Time)):
				continue
			case <-ctx.Done():
				return ctx.Err()
			}
		} else if err != nil {
			return errors.WithStack(err)
		}

		for i := range issues {
			if err := w.listComments(ctx, issues[i]); err != nil {
				return err
			}

			if err := w.store.insertIssue(ctx, issues[i]); err != nil {
				return err
			}
		}

		if resp.NextPage <= opts.ListOptions.Page {
			break
		}
		opts.ListOptions.Page = resp.NextPage
	}
	return nil
}

var issueURLRegexp = regexp.MustCompile(`^https://api\.github\.com/repos/([a-zA-Z\d\._-]+)/([a-zA-Z\d\._-]+)/issues/(\d+)$`)

func (w *worker) listComments(ctx context.Context, issue *github.Issue) error {
	// Don't list issue comments if the issue is already stored up-to-date with all comments
	existing, err := w.store.getIssue(ctx, issue.GetID())
	if err != nil {
		return err
	}
	if existing.GetUpdatedAt().Equal(issue.GetUpdatedAt()) {
		count, err := w.store.countCommentsForIssue(ctx, issue)
		if err != nil {
			return err
		}
		if issue.GetComments() <= count {
			return nil
		}
	}

	match := issueURLRegexp.FindStringSubmatch(issue.GetURL())
	if len(match) < 4 {
		return errors.Errorf("couldn't match %s", issue.GetURL())
	}
	owner := match[1]
	repo := match[2]
	number, err := strconv.Atoi(match[3])
	if err != nil {
		return errors.WithStack(err)
	}

	opts := &github.IssueListCommentsOptions{ListOptions: github.ListOptions{PerPage: 100}}
	for {
		comments, resp, err := w.githubClient.Issues.ListComments(ctx, owner, repo, number, opts)
		if resp != nil {
			b, _ := json.Marshal(resp.Rate)
			if err := w.cache.Set("github-core-rate", b, time.Until(resp.Reset.Time)); err != nil {
				log.Print(err)
			}
			if err := w.cache.LPush("github-requests", &githubRequest{
				Timestamp: time.Now(), HTTPStatus: resp.StatusCode, Page: opts.Page, LastPage: resp.LastPage, Description: fmt.Sprintf("list #%d comments", issue.GetNumber()),
			}); err != nil {
				log.Print(err)
			}
		}
		if _, ok := err.(*github.RateLimitError); ok {
			select {
			case <-time.After(time.Until(resp.Reset.Time)):
				continue
			case <-ctx.Done():
				return ctx.Err()
			}
		} else if err != nil {
			return errors.WithStack(err)
		}

		for i := range comments {
			if err := w.store.insertComment(ctx, comments[i]); err != nil {
				return err
			}
		}

		if resp.NextPage <= opts.ListOptions.Page {
			break
		}
		opts.ListOptions.Page = resp.NextPage
	}
	return nil
}
