package main

import (
	"context"
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

type fetcher struct {
	broker       *broker
	cache        *cache
	store        *store
	githubClient *github.Client
}

func newFetcher(broker *broker, cache *cache, store *store, githubClient *github.Client) *fetcher {
	return &fetcher{broker: broker, cache: cache, store: store, githubClient: githubClient}
}

func (f *fetcher) fetch(ctx context.Context, b []byte) error {
	var p payload
	if err := json.Unmarshal(b, &p); err != nil {
		return errors.WithStack(err)
	}

	var ferr error
	switch p.Type {
	case "user":
		var u userPayload
		if err := json.Unmarshal(p.Payload, &u); err != nil {
			return errors.WithStack(err)
		}
		ferr = f.fetchUser(ctx, u)
	case "issue":
		var i issuePayload
		if err := json.Unmarshal(p.Payload, &i); err != nil {
			return errors.WithStack(err)
		}
		ferr = f.fetchIssue(ctx, i)
	case "repo":
		var r repoPayload
		if err := json.Unmarshal(p.Payload, &r); err != nil {
			return errors.WithStack(err)
		}
		ferr = f.fetchRepo(ctx, r)
	default:
		return errors.Errorf("don't know what to do with payload of type %v", p.Type)
	}

	if rlerr, ok := errors.Cause(ferr).(*github.RateLimitError); ok {
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(time.Until(rlerr.Rate.Reset.Time)):
			// retry
			return f.fetch(ctx, b)
		}
	}
	return ferr
}

func (f *fetcher) fetchRepo(ctx context.Context, repo repoPayload) error {
	// TODO: order by reactions?
	opts := &github.IssueListByRepoOptions{Sort: "updated", State: "all", ListOptions: github.ListOptions{Page: repo.Page, PerPage: 100}}
	start := time.Now()
	issues, resp, err := f.githubClient.Issues.ListByRepo(ctx, repo.Owner, repo.Name, opts)
	duration := time.Since(start)
	if resp != nil {
		if err := f.cache.updateRate("github-core-rate", resp.Rate); err != nil {
			log.Printf("%+v", err)
		}
		if err := f.cache.sendToRequestLog(fmt.Sprintf("list %s/%s issues", repo.Owner, repo.Name), opts.ListOptions, resp, duration); err != nil {
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

		if err := f.broker.Publish("queue-fetch", issuePayload{URL: issue.GetURL()}); err != nil {
			return err
		}
	}

	if resp.NextPage > opts.ListOptions.Page {
		return f.broker.Publish("queue-fetch", repoPayload{Owner: repo.Owner, Name: repo.Name, Page: resp.NextPage})
	}
	return nil
}

func (f *fetcher) fetchUser(ctx context.Context, user userPayload) error {
	query := fmt.Sprintf(`commenter:"%s"`, user.Login)
	opts := &github.SearchOptions{Sort: "updated", Order: "desc", ListOptions: github.ListOptions{Page: user.Page, PerPage: 100}}
	start := time.Now()
	result, resp, err := f.githubClient.Search.Issues(ctx, query, opts)
	duration := time.Since(start)
	if resp != nil {
		if err := f.cache.updateRate("github-search-rate", resp.Rate); err != nil {
			log.Printf("%+v", err)
		}
		if err := f.cache.sendToRequestLog(fmt.Sprintf("search issues commented by %s", user.Login), opts.ListOptions, resp, duration); err != nil {
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

		if err := f.broker.Publish("queue-fetch", issuePayload{URL: issue.GetURL()}); err != nil {
			return err
		}
	}

	if resp.NextPage > opts.ListOptions.Page {
		return f.broker.Publish("queue-fetch", userPayload{Login: user.Login, Page: resp.NextPage})
	}
	return nil
}

var issueURLRegexp = regexp.MustCompile(`^https://api\.github\.com/repos/([\w-]+)/([\w\.-]+)/issues/(\d+)$`)

func (f *fetcher) fetchIssue(ctx context.Context, issue issuePayload) error {
	match := issueURLRegexp.FindStringSubmatch(issue.URL)
	if len(match) < 4 {
		return errors.Errorf("couldn't match %s", issue.URL)
	}
	owner := match[1]
	repo := match[2]
	number, err := strconv.Atoi(match[3])
	if err != nil {
		return errors.WithStack(err)
	}

	opts := &github.IssueListCommentsOptions{ListOptions: github.ListOptions{Page: issue.Page, PerPage: 100}}
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
		return f.broker.Publish("queue-fetch", issuePayload{URL: issue.URL, Page: resp.NextPage})
	}
	return nil
}
