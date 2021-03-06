package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"

	"github.com/google/go-github/github"
	"github.com/jmoiron/sqlx"
	"github.com/pkg/errors"
)

func newStore(db *sqlx.DB) *store {
	return &store{db: db}
}

type store struct{ db *sqlx.DB }

type comment struct {
	Comment github.IssueComment
	Repo    string
}

func (s *store) getComments(ctx context.Context) ([]comment, error) {
	var dest []struct {
		J    []byte
		Repo string
	}
	if err := s.db.SelectContext(ctx, &dest,
		`select j, repo from comments
		where (j#>>'{reactions,total_count}')::int > 0
		order by (j#>>'{reactions,total_count}')::int desc limit 100`,
	); err != nil {
		return nil, errors.WithStack(err)
	}

	comments := make([]comment, len(dest))
	for i := range dest {
		if err := json.Unmarshal(dest[i].J, &comments[i].Comment); err != nil {
			return nil, errors.WithStack(err)
		}
		comments[i].Repo = dest[i].Repo
	}
	return comments, nil
}

func (s *store) getCommentsForUser(ctx context.Context, user string) ([]comment, error) {
	var dest []struct {
		J    []byte
		Repo string
	}
	if err := s.db.SelectContext(ctx, &dest,
		`select j, repo from comments
		where (j#>>'{reactions,total_count}')::int > 0 and j#>>'{user,login}' = $1
		order by (j#>>'{reactions,total_count}')::int desc limit 100`,
		user,
	); err != nil {
		return nil, errors.WithStack(err)
	}

	comments := make([]comment, len(dest))
	for i := range dest {
		if err := json.Unmarshal(dest[i].J, &comments[i].Comment); err != nil {
			return nil, errors.WithStack(err)
		}
		comments[i].Repo = dest[i].Repo
	}
	return comments, nil
}

func (s *store) getCommentsForRepo(ctx context.Context, owner, repo string) ([]comment, error) {
	var dest []struct {
		J    []byte
		Repo string
	}
	if err := s.db.SelectContext(ctx, &dest,
		`select j, repo from comments
		where (j#>>'{reactions,total_count}')::int > 0 and repo = $1
		order by (j#>>'{reactions,total_count}')::int desc limit 100`,
		strings.Join([]string{owner, repo}, "/"),
	); err != nil {
		return nil, errors.WithStack(err)
	}

	comments := make([]comment, len(dest))
	for i := range dest {
		if err := json.Unmarshal(dest[i].J, &comments[i].Comment); err != nil {
			return nil, errors.WithStack(err)
		}
		comments[i].Repo = dest[i].Repo
	}
	return comments, nil
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
	err := s.db.GetContext(ctx, &count,
		`select count(*) from comments where j->>'issue_url' = $1`,
		issue.GetURL(),
	)
	return count, errors.WithStack(err)
}

func (s *store) getIssue(ctx context.Context, id int64) (*github.Issue, error) {
	var dest []byte
	if err := s.db.GetContext(ctx, &dest, `select j from issues where (j->>'id')::int = $1`, id); err == sql.ErrNoRows {
		return nil, nil
	} else if err != nil {
		return nil, errors.WithStack(err)
	}
	var issue github.Issue
	err := json.Unmarshal(dest, &issue)
	return &issue, errors.WithStack(err)
}

func (s *store) insertComment(ctx context.Context, comment *github.IssueComment, repo string) error {
	j, err := json.Marshal(comment)
	if err != nil {
		return errors.WithStack(err)
	}
	_, err = s.db.ExecContext(ctx, `insert into comments values($1, $2)
	on conflict (((j->>'id')::int)) do update
	set j = excluded.j
	where (comments.j->>'updated_at')::timestamp < (excluded.j->>'updated_at')::timestamp`, j, repo)
	return errors.Wrapf(err, "couldn't insert comment %s", comment.GetURL())
}

func (s *store) insertIssue(ctx context.Context, issue *github.Issue) error {
	j, err := json.Marshal(issue)
	if err != nil {
		return errors.WithStack(err)
	}
	_, err = s.db.ExecContext(ctx, `insert into issues values($1)
	on conflict (((j->>'id')::int)) do update
	set j = excluded.j
	where (issues.j->>'updated_at')::timestamp < (excluded.j->>'updated_at')::timestamp`, j)
	return errors.Wrapf(err, "couldn't insert issue %s", issue.GetURL())
}
