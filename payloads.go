package main

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/go-github/github"
)

type payload struct {
	Type        string
	PublishedAt time.Time
	Payload     json.RawMessage
}

type repoPayload struct {
	Owner, Name string
	Page        int
}

func (r repoPayload) MarshalBinary() ([]byte, error) {
	raw, _ := json.Marshal(r)
	return json.Marshal(payload{Type: "repo", PublishedAt: time.Now(), Payload: raw})
}

type userPayload struct {
	Login string
	Page  int
}

func (u userPayload) MarshalBinary() ([]byte, error) {
	raw, _ := json.Marshal(u)
	return json.Marshal(payload{Type: "user", PublishedAt: time.Now(), Payload: raw})
}

type issuePayload struct {
	URL  string
	Page int
}

func (i issuePayload) MarshalBinary() ([]byte, error) {
	raw, _ := json.Marshal(i)
	return json.Marshal(payload{Type: "issue", PublishedAt: time.Now(), Payload: raw})
}

type githubRate github.Rate

func (r githubRate) MarshalBinary() ([]byte, error) { return json.Marshal(r) }

type githubRequest struct {
	ID          int64
	Timestamp   time.Time
	Message     string
	ListOptions github.ListOptions
	StatusCode  int
	LastPage    int
	Duration    time.Duration
}

func (r *githubRequest) MarshalBinary() ([]byte, error) { return json.Marshal(r) }

func (r githubRequest) String() string {
	message := r.Message
	if r.StatusCode == 200 {
		page := r.ListOptions.Page
		if page == 0 {
			page = 1
		}
		lastPage := r.LastPage
		if lastPage == 0 {
			lastPage = page
		}
		message = fmt.Sprintf("%s (%d/%d)", message, page, lastPage)
	}
	return fmt.Sprintf("ts=%s msg=%q status=%d duration=%s", r.Timestamp.Format(time.RFC3339), message, r.StatusCode, r.Duration)
}
