package contrib

import "time"

// Kind enumerates the contribution types a single Event can represent.
type Kind int

const (
	KindCommit Kind = iota
	KindIssue
	KindPR
	KindReview
	KindComment
)

// Event is one contribution: a login acting at a point in time. An empty
// Login means the underlying commit/comment has no linked GitHub account.
type Event struct {
	Login string    `json:"login"`
	Kind  Kind      `json:"kind"`
	At    time.Time `json:"at"`
}
