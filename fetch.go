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
	"github.com/pkg/errors"
)

func fetch(ctx context.Context, cfg *config) {
	f := &fetcher{store: cfg.store, cache: cfg.cache, githubClient: cfg.githubClient}
	for {
		err := f.work(ctx)
		cause := errors.Cause(err)
		if cause == sql.ErrNoRows || cause == context.Canceled {
			return
		} else if rlerr, ok := cause.(*github.RateLimitError); ok {
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

type fetcher struct {
	store        *store
	cache        *cache
	githubClient *github.Client
}

func (f *fetcher) work(ctx context.Context) error {
	// TODO: abstract db transaction in store
	tx, err := f.store.db.BeginTxx(ctx, nil)
	if err != nil {
		return errors.WithStack(err)
	}
	defer func() {
		if err := tx.Rollback(); err != sql.ErrTxDone && err != nil {
			log.Printf("%+v", errors.WithStack(err))
		}
	}()

	var fetchItem struct {
		ID      int64
		Type    string
		Payload []byte
		Retry   int
	}
	if err := tx.Get(&fetchItem, `select id, type, payload, retry from fetch_queue order by created_at limit 1 for update skip locked`); err != nil {
		return errors.WithStack(err)
	}

	var fetchErr error
	switch fetchItem.Type {
	case "repo":
		var repo repoFetchItem
		if err := json.Unmarshal(fetchItem.Payload, &repo); err != nil {
			return errors.WithStack(err)
		}
		fetchErr = f.listIssues(ctx, repo.Owner, repo.Name, repo.Page)
	case "user":
		var user userFetchItem
		if err := json.Unmarshal(fetchItem.Payload, &user); err != nil {
			return errors.WithStack(err)
		}
		fetchErr = f.searchIssues(ctx, user.Login, user.Page)
	case "issue":
		var issue issueFetchItem
		if err := json.Unmarshal(fetchItem.Payload, &issue); err != nil {
			return errors.WithStack(err)
		}
		fetchErr = f.listComments(ctx, issue.URL, issue.Page)
	default:
		fetchErr = errors.Errorf("don't know what to do with fetch of type %s", fetchItem.Type)
	}

	if _, err := tx.Exec(`delete from fetch_queue where id = $1`, fetchItem.ID); err != nil {
		return errors.WithStack(err)
	}
	if err := f.cache.updateCount(fetchItem.Type, -1); err != nil {
		return err
	}

	if fetchErr != nil {
		if fetchItem.Retry <= 2 {
			if _, err := tx.Exec(`insert into fetch_queue(type, payload, retry) values($1, $2, $3)`, fetchItem.Type, fetchItem.Payload, fetchItem.Retry+1); err != nil {
				return errors.WithStack(err)
			}
			if err := f.cache.updateCount(fetchItem.Type, 1); err != nil {
				return err
			}
		} else {
			// TODO: alert?
		}
	}
	if err := tx.Commit(); err != nil {
		return errors.WithStack(err)
	}
	return fetchErr
}

func (f *fetcher) searchIssues(ctx context.Context, user string, page int) error {
	query := fmt.Sprintf(`commenter:"%s"`, user)
	opts := &github.SearchOptions{Sort: "updated", Order: "desc", ListOptions: github.ListOptions{Page: page, PerPage: 100}}
	start := time.Now()
	result, resp, err := f.githubClient.Search.Issues(ctx, query, opts)
	duration := time.Since(start)
	if resp != nil {
		if err := f.cache.updateRate("github-search-rate", resp.Rate); err != nil {
			log.Printf("%+v", err)
		}
		if err := f.cache.sendToRequestLog(fmt.Sprintf("search issues commented by %s", user), opts.ListOptions, resp, duration); err != nil {
			log.Printf("%+v", err)
		}
	}
	if err != nil {
		return errors.WithStack(err)
	}

	for i := range result.Issues {
		issue := result.Issues[i]
		if ok, err := f.store.issueIsUpToDate(ctx, &issue); err != nil {
			return err
		} else if ok {
			continue
		}

		if err := f.store.insertIssue(ctx, &issue); err != nil {
			return err
		}

		if err := f.store.addFetchItemToQueue(ctx, issueFetchItem{URL: issue.GetURL()}); err != nil {
			return err
		}
	}

	if resp.NextPage > opts.ListOptions.Page {
		return f.store.addFetchItemToQueue(ctx, userFetchItem{Login: user, Page: resp.NextPage})
	}
	return nil
}

func (f *fetcher) listIssues(ctx context.Context, owner, repo string, page int) error {
	// TODO: order by reactions?
	opts := &github.IssueListByRepoOptions{Sort: "updated", State: "all", ListOptions: github.ListOptions{Page: page, PerPage: 100}}
	start := time.Now()
	issues, resp, err := f.githubClient.Issues.ListByRepo(ctx, owner, repo, opts)
	duration := time.Since(start)
	if resp != nil {
		if err := f.cache.updateRate("github-core-rate", resp.Rate); err != nil {
			log.Printf("%+v", err)
		}
		if err := f.cache.sendToRequestLog(fmt.Sprintf("list %s/%s issues", owner, repo), opts.ListOptions, resp, duration); err != nil {
			log.Printf("%+v", err)
		}
	}
	if err != nil {
		return errors.WithStack(err)
	}

	for i := range issues {
		issue := issues[i]
		if ok, err := f.store.issueIsUpToDate(ctx, issue); err != nil {
			return err
		} else if ok {
			continue
		}

		if err := f.store.insertIssue(ctx, issues[i]); err != nil {
			return err
		}

		if err := f.store.addFetchItemToQueue(ctx, issueFetchItem{URL: issue.GetURL()}); err != nil {
			return err
		}
	}

	if resp.NextPage > opts.ListOptions.Page {
		return f.store.addFetchItemToQueue(ctx, repoFetchItem{Owner: owner, Name: repo, Page: resp.NextPage})
	}
	return nil
}

var issueURLRegexp = regexp.MustCompile(`^https://api\.github\.com/repos/([\w-]+)/([\w\.-]+)/issues/(\d+)$`)

func (f *fetcher) listComments(ctx context.Context, issueURL string, page int) error {
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
	comments, resp, err := f.githubClient.Issues.ListComments(ctx, owner, repo, number, opts)
	duration := time.Since(start)
	if resp != nil {
		if err := f.cache.updateRate("github-core-rate", resp.Rate); err != nil {
			log.Printf("%+v", err)
		}
		if err := f.cache.sendToRequestLog(fmt.Sprintf("list %s/%s#%d comments", owner, repo, number), opts.ListOptions, resp, duration); err != nil {
			log.Printf("%+v", err)
		}
	}
	if err != nil {
		return errors.WithStack(err)
	}

	for i := range comments {
		if err := f.store.insertComment(ctx, comments[i], strings.Join([]string{owner, repo}, "/")); err != nil {
			return err
		}
	}

	if resp.NextPage > opts.ListOptions.Page {
		return f.store.addFetchItemToQueue(ctx, issueFetchItem{URL: issueURL, Page: resp.NextPage})
	}
	return nil
}
