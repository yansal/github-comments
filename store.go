package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/google/go-github/github"
	"github.com/jmoiron/sqlx"
	"github.com/pkg/errors"
)

func newStore(db *sqlx.DB) *store {
	return &store{db}
}

type store struct{ *sqlx.DB }

func (s *store) getComments(ctx context.Context) ([]github.IssueComment, error) {
	var dest [][]byte
	if err := s.Select(&dest,
		`select j from comments
		where (j#>>'{reactions,total_count}')::int > 0
		order by (j#>>'{reactions,total_count}')::int desc limit 100`,
	); err != nil {
		return nil, errors.WithStack(err)
	}
	return unmarshalComments(dest)
}

func (s *store) getCommentsForUser(ctx context.Context, user string) ([]github.IssueComment, error) {
	var dest [][]byte
	if err := s.Select(&dest,
		`select j from comments
		where (j#>>'{reactions,total_count}')::int > 0 and j#>>'{user,login}' = $1
		order by (j#>>'{reactions,total_count}')::int desc limit 100`,
		user,
	); err != nil {
		return nil, errors.WithStack(err)
	}
	return unmarshalComments(dest)
}

func (s *store) getCommentsForRepo(ctx context.Context, owner, repo string) ([]github.IssueComment, error) {
	var dest [][]byte
	if err := s.Select(&dest,
		`select j from comments
		where (j#>>'{reactions,total_count}')::int > 0 and j->>'html_url' like $1
		order by (j#>>'{reactions,total_count}')::int desc limit 100`,
		fmt.Sprintf("https://github.com/%s/%s/%%", owner, repo),
	); err != nil {
		return nil, errors.WithStack(err)
	}
	return unmarshalComments(dest)
}

func (s *store) issueIsUpToDate(ctx context.Context, issue *github.Issue) (bool, error) {
	existing, err := s.getIssue(ctx, issue.GetID())
	if err != nil {
		return false, err
	}
	if existing.GetUpdatedAt().Equal(issue.GetUpdatedAt()) {
		count, err := s.countCommentsForIssue(ctx, issue)
		if err != nil {
			return false, err
		}
		if issue.GetComments() == count {
			return true, nil
		}
	}
	return false, nil
}

func (s *store) countCommentsForIssue(ctx context.Context, issue *github.Issue) (int, error) {
	var count int
	err := s.Get(&count,
		`select count(*) from comments where j->>'issue_url' = $1`,
		issue.GetURL(),
	)
	return count, errors.WithStack(err)
}

func unmarshalComments(b [][]byte) ([]github.IssueComment, error) {
	comments := make([]github.IssueComment, len(b))
	for i := range b {
		if err := json.Unmarshal(b[i], &comments[i]); err != nil {
			return nil, errors.WithStack(err)
		}
	}
	return comments, nil
}

func (s *store) getIssue(ctx context.Context, id int64) (*github.Issue, error) {
	var dest []byte
	if err := s.Get(&dest, `select j from issues where (j->>'id')::int = $1`, id); err == sql.ErrNoRows {
		return nil, nil
	} else if err != nil {
		return nil, errors.WithStack(err)
	}
	var issue github.Issue
	err := json.Unmarshal(dest, &issue)
	return &issue, errors.WithStack(err)
}

func (s *store) insertComment(ctx context.Context, comment *github.IssueComment) error {
	j, err := json.Marshal(comment)
	if err != nil {
		return errors.WithStack(err)
	}
	_, err = s.Exec(`insert into comments values($1)
	on conflict (((j->>'id')::int)) do update
	set j = excluded.j
	where (comments.j->>'updated_at')::timestamp < (excluded.j->>'updated_at')::timestamp`, j)
	return errors.Wrapf(err, "couldn't insert comment %s", comment.GetURL())
}

func (s *store) insertIssue(ctx context.Context, issue *github.Issue) error {
	j, err := json.Marshal(issue)
	if err != nil {
		return errors.WithStack(err)
	}
	_, err = s.Exec(`insert into issues values($1)
	on conflict (((j->>'id')::int)) do update
	set j = excluded.j
	where (issues.j->>'updated_at')::timestamp < (excluded.j->>'updated_at')::timestamp`, j)
	return errors.WithStack(err)
}

type jobRepo struct {
	Owner, Name string
	Page        int
}

func (s *store) insertJobRepo(ctx context.Context, repo jobRepo) error {
	b, _ := json.Marshal(repo)
	_, err := s.Exec(`insert into jobs(type, payload) values($1, $2) on conflict do nothing`, "repo", b)
	return errors.WithStack(err)
}

type jobIssue struct {
	URL  string
	Page int
}

func (s *store) insertJobIssue(ctx context.Context, issue jobIssue) error {
	b, _ := json.Marshal(issue)
	_, err := s.Exec(`insert into jobs(type, payload) values($1, $2) on conflict do nothing`, "issue", b)
	return errors.WithStack(err)
}

type jobUser struct {
	Login string
	Page  int
}

func (s *store) insertJobUser(ctx context.Context, user jobUser) error {
	b, _ := json.Marshal(user)
	_, err := s.Exec(`insert into jobs(type, payload) values($1, $2) on conflict do nothing`, "user", b)
	return errors.WithStack(err)
}
