// Package history is the "have I seen this before?" layer. It embeds an error
// (via Ollama), searches a local Qdrant collection for similar past errors, and
// stores each error plus its prior analysis (cause/fixes) so future matches can
// recall how a recurring error was resolved. It is an orchestrator over the
// vector.Store and ollama embedding interfaces and is fully unit-testable with
// fakes; it owns no I/O of its own.
package history

import (
	"context"
	"fmt"
	"hash/fnv"
	"strings"
	"time"

	"github.com/gvleverett/error-explainer/internal/analyze"
	"github.com/gvleverett/error-explainer/internal/render"
	"github.com/gvleverett/error-explainer/internal/vector"
)

// Embedder is the subset of the Ollama client history needs: producing an
// embedding vector for a prompt. ollama.Client satisfies this directly.
type Embedder interface {
	Embed(ctx context.Context, model, prompt string) ([]float64, error)
}

// History ties an embedder to a vector store plus the tuning knobs that the CLI
// exposes as flags. A zero History is not usable; construct it in cmd/root.go.
type History struct {
	Store      vector.Store
	Embedder   Embedder
	EmbedModel string
	Collection string
	Threshold  float64
	TopK       int
}

// Prior is one recalled past occurrence: how similar it scored, how many times
// it has been seen, when it was last seen, and the prior PROBABLE CAUSE /
// POTENTIAL FIXES captured from the last analysis.
type Prior struct {
	Score    float64
	Count    int
	LastSeen time.Time
	Cause    string
	Fixes    string
}

// Recall embeds embedText, ensures the collection exists at the embedding's
// dimension, and searches for the TopK nearest points above Threshold. It maps
// the returned payloads to Prior values. An empty (not error) result means no
// similar past errors were found.
func (h *History) Recall(ctx context.Context, embedText string) ([]Prior, error) {
	embed, err := h.Embedder.Embed(ctx, h.EmbedModel, embedText)
	if err != nil {
		return nil, err
	}
	if len(embed) == 0 {
		return nil, fmt.Errorf("embedding model returned an empty vector")
	}
	if err := h.Store.EnsureCollection(ctx, h.Collection, len(embed)); err != nil {
		return nil, err
	}
	matches, err := h.Store.Search(ctx, h.Collection, embed, h.TopK, h.Threshold)
	if err != nil {
		return nil, err
	}
	priors := make([]Prior, 0, len(matches))
	for _, m := range matches {
		priors = append(priors, Prior{
			Score:    m.Score,
			Count:    int(toInt(m.Payload["count"])),
			LastSeen: toTime(m.Payload["last_seen"]),
			Cause:    toStr(m.Payload["cause"]),
			Fixes:    toStr(m.Payload["fixes"]),
		})
	}
	return priors, nil
}

// Record stores (or increments) an error in the collection. The point id is the
// FNV-64a hash of the normalized signature so the same error family converges
// to one point. It reads any existing point to preserve first_seen and bump the
// count, sets last_seen to now, and upserts the point with the same embedding.
// embed may be nil (the caller can pass the recall-time embedding to avoid a
// second embed call); when nil, Record re-embeds the representative text.
func (h *History) Record(ctx context.Context, sig, rep, lang, cause, fixes, chatModel string, embed []float64) error {
	if sig == "" {
		return fmt.Errorf("refusing to store an empty signature")
	}
	if embed == nil {
		var err error
		embed, err = h.Embedder.Embed(ctx, h.EmbedModel, rep)
		if err != nil {
			return err
		}
	}
	if len(embed) == 0 {
		return fmt.Errorf("embedding model returned an empty vector")
	}
	if err := h.Store.EnsureCollection(ctx, h.Collection, len(embed)); err != nil {
		return err
	}

	id := pointID(sig)
	now := time.Now().UTC()

	count := 1
	firstSeen := now
	if payload, err := h.Store.GetPoint(ctx, h.Collection, id); err != nil {
		return err
	} else if payload != nil {
		count = int(toInt(payload["count"])) + 1
		if t := toTime(payload["first_seen"]); !t.IsZero() {
			firstSeen = t
		}
	}

	payload := map[string]any{
		"signature":   sig,
		"representative": rep,
		"language":     lang,
		"cause":        cause,
		"fixes":        fixes,
		"model":        chatModel,
		"count":        count,
		"first_seen":   firstSeen.Format(time.RFC3339),
		"last_seen":    now.Format(time.RFC3339),
	}
	return h.Store.UpsertPoint(ctx, h.Collection, id, embed, payload)
}

// FormatBlock renders priors as the prompt's PRIOR OCCURRENCES block. It
// returns "" when there are none, so the caller can omit the block entirely.
func FormatBlock(priors []Prior) string {
	if len(priors) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("--- PRIOR OCCURRENCES ---\n")
	for i, p := range priors {
		fmt.Fprintf(&b, "[%d] similarity %.2f, seen %d time(s), last %s\n",
			i+1, p.Score, p.Count, p.LastSeen.Format("2006-01-02"))
		if p.Cause != "" {
			fmt.Fprintf(&b, "PROBABLE CAUSE: %s\n", excerpt(p.Cause, 500))
		}
		if p.Fixes != "" {
			fmt.Fprintf(&b, "POTENTIAL FIXES: %s\n", excerpt(p.Fixes, 500))
		}
	}
	b.WriteString("--- END PRIOR OCCURRENCES ---")
	return b.String()
}

// ParseSummary extracts the PROBABLE CAUSE and POTENTIAL FIXES sections from the
// model's analysis so they can be stored alongside the error for future recall.
// It reuses render.Parse read-only. Each is capped at 1000 chars.
func ParseSummary(content string) (cause, fixes string) {
	for _, s := range render.Parse(content) {
		switch s.Title {
		case "PROBABLE CAUSE":
			cause = s.Body
		case "POTENTIAL FIXES":
			fixes = s.Body
		}
	}
	return excerpt(cause, 1000), excerpt(fixes, 1000)
}

// EmbedText picks the text to embed for similarity search: the representative of
// the top error group when grouping finds one (a stable, normalized view of the
// error), else the first 4KB of the raw input. It never embeds the full 256KB
// input — that would drown the signature in unrelated log noise.
func EmbedText(ctx *analyze.Context, raw string) string {
	if ctx != nil && len(ctx.ErrorGroups) > 0 {
		return ctx.ErrorGroups[0].Representative
	}
	if len(raw) > 4096 {
		return raw[:4096]
	}
	return raw
}

// pointID is the deterministic uint64 id for a signature (FNV-64a).
func pointID(sig string) uint64 {
	h := fnv.New64a()
	h.Write([]byte(sig))
	return h.Sum64()
}

// excerpt truncates s to max chars, appending an ellipsis when cut.
func excerpt(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

// toInt coerces a Qdrant payload number (float64 from JSON) to an int.
func toInt(v any) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case int:
		return int64(n)
	case int64:
		return n
	}
	return 0
}

// toStr coerces a Qdrant payload value to a string.
func toStr(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// toTime parses an RFC3339 timestamp from the payload, returning the zero time
// on failure (so IsZero checks keep first_seen sane).
func toTime(v any) time.Time {
	s, ok := v.(string)
	if !ok {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}