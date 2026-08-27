package news

import (
	"context"
	"time"
)

type Kind string

const (
	StockNews    Kind = "stock_news"
	PressRelease Kind = "press_release"
)

type Article struct {
	ID          string    `json:"id"`
	Sequence    int64     `json:"sequence"`
	Kind        Kind      `json:"kind"`
	Symbols     []string  `json:"symbols"`
	Title       string    `json:"title"`
	Summary     string    `json:"summary,omitempty"`
	URL         string    `json:"url"`
	ImageURL    string    `json:"image_url,omitempty"`
	Publisher   string    `json:"publisher,omitempty"`
	PublishedAt time.Time `json:"published_at"`
	ReceivedAt  time.Time `json:"received_at"`
	Provider    string    `json:"provider"`
}

type Event struct {
	Type     string   `json:"type"`
	Action   string   `json:"action,omitempty"`
	Sequence int64    `json:"sequence,omitempty"`
	Article  *Article `json:"article,omitempty"`
	State    string   `json:"state,omitempty"`
	Detail   string   `json:"detail,omitempty"`
	Reason   string   `json:"reason,omitempty"`
}

type Query struct {
	Symbols        []string
	Kinds          []Kind
	AfterSequence  int64
	BeforeSequence int64
	UntilSequence  int64
	Limit          int
}

type ListResponse struct {
	News               []Article `json:"news"`
	LatestSequence     int64     `json:"latest_sequence"`
	NextBeforeSequence int64     `json:"next_before_sequence,omitempty"`
}

type Provider interface {
	Name() string
	Latest(ctx context.Context, kind Kind, page, limit int) ([]Article, error)
}
