// Package vector is a stdlib-only Qdrant REST client used by the history
// feature. It defines a Store interface (so the history orchestrator can be
// unit-tested with a fake) and a QdrantClient that implements it over net/http.
// Qdrant is a soft, external dependency: when unreachable, callers skip
// history gracefully.
package vector

import (
	"context"
	"errors"
	"net/http"
	"time"
)

// Match is one nearest point found by Search, with its similarity score and
// stored payload.
type Match struct {
	Score   float64
	Payload map[string]any
}

// Store is the subset of Qdrant operations the history feature needs.
type Store interface {
	// EnsureCollection creates the collection with the given vector dimension
	// if it does not exist. If it already exists, EnsureCollection returns nil
	// (assuming the existing dimension is correct); a dimension mismatch is
	// reported as an error rather than silently recreated.
	EnsureCollection(ctx context.Context, name string, dim int) error

	// Search returns up to limit nearest points to vec whose score is at least
	// scoreThreshold, with their payloads.
	Search(ctx context.Context, name string, vec []float64, limit int, scoreThreshold float64) ([]Match, error)

	// GetPoint returns the payload of the point with the given uint64 id, or
	// (nil, nil) if it does not exist (404 is treated as not-found, not error).
	GetPoint(ctx context.Context, name string, id uint64) (map[string]any, error)

	// UpsertPoint writes (or replaces) the point with the given id, vector, and
	// payload. Qdrant replaces the whole point, so the vector must be re-passed.
	UpsertPoint(ctx context.Context, name string, id uint64, vec []float64, payload map[string]any) error
}

// ErrUnreachable wraps a network/DNS error (Qdrant down or wrong host).
type ErrUnreachable struct{ Err error }

func (e *ErrUnreachable) Error() string { return "qdrant unreachable: " + e.Err.Error() }
func (e *ErrUnreachable) Unwrap() error { return e.Err }

// IsUnreachable reports whether err is an ErrUnreachable.
func IsUnreachable(err error) bool {
	var u *ErrUnreachable
	return errors.As(err, &u)
}

// ErrDimMismatch is returned when an existing collection's vector size differs
// from the requested dimension.
type ErrDimMismatch struct {
	Collection string
	Have, Want int
}

func (e *ErrDimMismatch) Error() string {
	return "collection " + e.Collection + " has vector size " + itoa(e.Have) + ", want " + itoa(e.Want)
}

// NewQdrant builds a QdrantClient targeting baseURL with the given timeout.
func NewQdrant(baseURL string, timeout time.Duration) *QdrantClient {
	return &QdrantClient{
		BaseURL: baseURL,
		HTTP:    &http.Client{Timeout: timeout},
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		b = append([]byte{'-'}, b...)
	}
	return string(b)
}