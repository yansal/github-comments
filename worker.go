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
		if err := w.work(ctx); errors.Cause(err) == sql.ErrNoRows || errors.Cause(err) == context.Canceled {
			return
		} else if rlerr, ok := errors.Cause(err).(*github.RateLimitError); ok {
			select {
			case <-time.After(time.Until(rlerr.Rate.Reset.Time)):
			case <-ctx.Done():
				return
			}
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
		Retry   int
	}
	if err := tx.Get(&job, `select id, type, payload, retry from jobs order by created_at limit 1 for update skip locked`); err != nil {
		return errors.WithStack(err)
	}

	var jerr error
	switch job.Type {
	case "repo":
		var repo jobRepo
		if err := json.Unmarshal(job.Payload, &repo); err != nil {
			return errors.WithStack(err)
		}
		jerr = w.listIssues(ctx, repo.Owner, repo.Name, repo.Page)
	case "user":
		var user jobUser
		if err := json.Unmarshal(job.Payload, &user); err != nil {
			return errors.WithStack(err)
		}
		jerr = w.searchIssues(ctx, user.Login, user.Page)
	case "issue":
		var issue jobIssue
		if err := json.Unmarshal(job.Payload, &issue); err != nil {
			return errors.WithStack(err)
		}
		jerr = w.listComments(ctx, issue.URL, issue.Page)
	default:
		jerr = errors.Errorf("don't know what to do with job of type %s", job.Type)
	}

	if _, err := tx.Exec(`delete from jobs where id = $1`, job.ID); err != nil {
		return errors.WithStack(err)
	}

	if jerr != nil {
		if job.Retry <= 2 {
			if _, err := tx.Exec(`insert into jobs(type, payload, retry) values($1, $2, $3)`, job.Type, job.Payload, job.Retry+1); err != nil {
				return errors.WithStack(err)
			}
		} else {
			// alert?
		}
	}
	if err := tx.Commit(); err != nil {
		return errors.WithStack(err)
	}
	return jerr
}

func (w *worker) searchIssues(ctx context.Context, user string, page int) error {
	query := fmt.Sprintf(`commenter:"%s"`, user)
	opts := &github.SearchOptions{Sort: "updated", Order: "desc", ListOptions: github.ListOptions{Page: page, PerPage: 100}}
	start := time.Now()
	result, resp, err := w.githubClient.Search.Issues(ctx, query, opts)
	duration := time.Since(start)
	if resp != nil {
		if err := w.cache.updateRate("github-search-rate", resp.Rate); err != nil {
			log.Print(err)
		}
		if err := w.cache.sendToRequestLog(fmt.Sprintf("search issues commented by %s", user), opts.ListOptions, resp, duration); err != nil {
			log.Print(err)
		}
	}
	if err != nil {
		return errors.WithStack(err)
	}

	for i := range result.Issues {
		issue := result.Issues[i]
		if ok, err := w.store.issueIsUpToDate(ctx, &issue); err != nil {
			return err
		} else if ok {
			continue
		}

		if err := w.store.insertJobIssue(ctx, jobIssue{URL: issue.GetURL()}); err != nil {
			return err
		}

		if err := w.store.insertIssue(ctx, &issue); err != nil {
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
	start := time.Now()
	issues, resp, err := w.githubClient.Issues.ListByRepo(ctx, owner, repo, opts)
	duration := time.Since(start)
	if resp != nil {
		if err := w.cache.updateRate("github-core-rate", resp.Rate); err != nil {
			log.Print(err)
		}
		if err := w.cache.sendToRequestLog(fmt.Sprintf("list %s/%s issues", owner, repo), opts.ListOptions, resp, duration); err != nil {
			log.Print(err)
		}
	}
	if err != nil {
		return errors.WithStack(err)
	}

	for i := range issues {
		issue := issues[i]
		if ok, err := w.store.issueIsUpToDate(ctx, issue); err != nil {
			return err
		} else if ok {
			continue
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
	start := time.Now()
	comments, resp, err := w.githubClient.Issues.ListComments(ctx, owner, repo, number, opts)
	duration := time.Since(start)
	if resp != nil {
		if err := w.cache.updateRate("github-core-rate", resp.Rate); err != nil {
			log.Print(err)
		}
		if err := w.cache.sendToRequestLog(fmt.Sprintf("list %s/%s#%d comments", owner, repo, number), opts.ListOptions, resp, duration); err != nil {
			log.Print(err)
		}
	}
	if err != nil {
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
