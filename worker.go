package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"strconv"
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
	if err := w.listener.Listen("jobs"); err != nil {
		return err
	}

	g, ctx := errgroup.WithContext(w.ctx)
	for i := 0; i < 2; i++ {
		g.Go(func() error {
			for {
				w.workLoop(ctx)
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

func (w *worker) workLoop(ctx context.Context) {
	for {
		if err := w.work(ctx); errors.Cause(err) == context.Canceled {
			return
		} else if err != nil {
			log.Printf("%+v", err)
		}
	}
}
func (w *worker) work(ctx context.Context) error {
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

	var job struct {
		ID      int64
		Type    string
		Payload []byte
	}
	if err := tx.Get(&job, `select id, type, payload from jobs order by created_at limit 1 for update skip locked`); err == sql.ErrNoRows {
		return nil
	} else if err != nil {
		return errors.WithStack(err)
	}

	switch job.Type {
	case "repo":
		var repo jobRepo
		if err := json.Unmarshal(job.Payload, &repo); err != nil {
			return errors.WithStack(err)
		}
		if err := w.listIssues(ctx, repo.Owner, repo.Name, repo.Page); err != nil {
			return err
		}
	case "user":
		var user jobUser
		if err := json.Unmarshal(job.Payload, &user); err != nil {
			return errors.WithStack(err)
		}
		if err := w.searchIssues(ctx, user.Login, user.Page); err != nil {
			return err
		}
	case "issue":
		var issue jobIssue
		if err := json.Unmarshal(job.Payload, &issue); err != nil {
			return errors.WithStack(err)
		}
		if err := w.listComments(ctx, issue.URL, issue.Page); err != nil {
			return err
		}
	default:
		return errors.Errorf("don't know what to do with job of type %s", job.Type)
	}

	if _, err := tx.Exec(`delete from jobs where id = $1`, job.ID); err != nil {
		return errors.WithStack(err)
	}
	return errors.WithStack(tx.Commit())
}

func (w *worker) searchIssues(ctx context.Context, user string, page int) error {
	query := fmt.Sprintf(`commenter:"%s"`, user)
	opts := &github.SearchOptions{Sort: "updated", Order: "desc", ListOptions: github.ListOptions{Page: page, PerPage: 100}}
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
			return w.store.insertJobUser(ctx, jobUser{Login: user, Page: page})
		case <-ctx.Done():
			return ctx.Err()
		}
	} else if err != nil {
		return errors.WithStack(err)
	}

	for i := range result.Issues {
		if err := w.store.insertJobIssue(ctx, jobIssue{URL: result.Issues[i].GetURL()}); err != nil {
			return err
		}

		if err := w.store.insertIssue(ctx, &result.Issues[i]); err != nil {
			return err
		}
	}

	if resp.NextPage > opts.ListOptions.Page {
		return w.store.insertJobUser(ctx, jobUser{Login: user, Page: resp.NextPage})
	}
	return nil
}

func (w *worker) listIssues(ctx context.Context, owner, repo string, page int) error {
	// TODO: order by reactions?
	opts := &github.IssueListByRepoOptions{Sort: "updated", State: "all", ListOptions: github.ListOptions{Page: page, PerPage: 100}}
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
			return w.store.insertJobRepo(ctx, jobRepo{Owner: owner, Name: repo, Page: page})
		case <-ctx.Done():
			return ctx.Err()
		}
	} else if err != nil {
		return errors.WithStack(err)
	}

	for i := range issues {
		issue := issues[i]
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

		if err := w.store.insertJobIssue(ctx, jobIssue{URL: issue.GetURL()}); err != nil {
			return err
		}

		if err := w.store.insertIssue(ctx, issues[i]); err != nil {
			return err
		}
	}

	if resp.NextPage > opts.ListOptions.Page {
		return w.store.insertJobRepo(ctx, jobRepo{Owner: owner, Name: repo, Page: resp.NextPage})
	}
	return nil
}

var (
	issueURLRegexp   = regexp.MustCompile(`^https://api\.github\.com/repos/([a-zA-Z\d\.-]+)/([a-zA-Z\d\._-]+)/issues/(\d+)$`)
	commentURLRegexp = regexp.MustCompile(`^https://api\.github\.com/repos/([a-zA-Z\d\.-]+)/([a-zA-Z\d\._-]+)/issues/comments/(\d+)$`)
)

func (w *worker) listComments(ctx context.Context, issueURL string, page int) error {
	match := issueURLRegexp.FindStringSubmatch(issueURL)
	if len(match) < 4 {
		return errors.Errorf("couldn't match %s", issueURL)
	}
	owner := match[1]
	repo := match[2]
	number, err := strconv.Atoi(match[3])
	if err != nil {
		return errors.WithStack(err)
	}

	opts := &github.IssueListCommentsOptions{ListOptions: github.ListOptions{Page: page, PerPage: 100}}
	comments, resp, err := w.githubClient.Issues.ListComments(ctx, owner, repo, number, opts)
	if resp != nil {
		b, _ := json.Marshal(resp.Rate)
		if err := w.cache.Set("github-core-rate", b, time.Until(resp.Reset.Time)); err != nil {
			log.Print(err)
		}
		if err := w.cache.LPush("github-requests", &githubRequest{
			Timestamp: time.Now(), HTTPStatus: resp.StatusCode, Page: opts.Page, LastPage: resp.LastPage, Description: fmt.Sprintf("list %s/%s#%d comments", owner, repo, number),
		}); err != nil {
			log.Print(err)
		}
	}

	if _, ok := err.(*github.RateLimitError); ok {
		select {
		case <-time.After(time.Until(resp.Reset.Time)):
			return w.store.insertJobIssue(ctx, jobIssue{URL: issueURL, Page: page})
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

	if resp.NextPage > opts.ListOptions.Page {
		return w.store.insertJobIssue(ctx, jobIssue{URL: issueURL, Page: resp.NextPage})
	}
	return nil
}
