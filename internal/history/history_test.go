package history

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gvleverett/error-explainer/internal/analyze"
	"github.com/gvleverett/error-explainer/internal/vector"
)

// --- fakes ---

type fakeEmbedder struct {
	vec  []float64
	err  error
	last string // last input embedded
}

func (f *fakeEmbedder) Embed(ctx context.Context, model, prompt string) ([]float64, error) {
	f.last = prompt
	if f.err != nil {
		return nil, f.err
	}
	return f.vec, nil
}

type fakeStore struct {
	dim       int
	created   bool
	searchErr error
	searchRes []vector.Match
	points    map[uint64]map[string]any
	upsertErr error
	lastVec   []float64
	lastPay   map[string]any
	lastID    uint64
	getErr    error
}

func (s *fakeStore) EnsureCollection(ctx context.Context, name string, dim int) error {
	s.created = true
	s.dim = dim
	return nil
}

func (s *fakeStore) Search(ctx context.Context, name string, vec []float64, limit int, threshold float64) ([]vector.Match, error) {
	if s.searchErr != nil {
		return nil, s.searchErr
	}
	return s.searchRes, nil
}

func (s *fakeStore) GetPoint(ctx context.Context, name string, id uint64) (map[string]any, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	if s.points == nil {
		return nil, nil
	}
	return s.points[id], nil // nil map entry → (nil, nil)
}

func (s *fakeStore) UpsertPoint(ctx context.Context, name string, id uint64, vec []float64, payload map[string]any) error {
	if s.upsertErr != nil {
		return s.upsertErr
	}
	if s.points == nil {
		s.points = map[uint64]map[string]any{}
	}
	s.points[id] = payload
	s.lastID = id
	s.lastVec = vec
	s.lastPay = payload
	return nil
}

func newHist(store *fakeStore, emb *fakeEmbedder) *History {
	return &History{
		Store:      store,
		Embedder:   emb,
		EmbedModel: "nomic-embed-text",
		Collection: "errs",
		Threshold:  0.85,
		TopK:       3,
	}
}

// --- tests ---

func TestRecall_Happy(t *testing.T) {
	store := &fakeStore{
		searchRes: []vector.Match{
			{Score: 0.92, Payload: map[string]any{
				"cause": "nil ptr", "fixes": "guard nil",
				"count": float64(2), "last_seen": "2026-08-05T10:00:00Z",
			}},
		},
	}
	emb := &fakeEmbedder{vec: []float64{0.1, 0.2, 0.3}}
	h := newHist(store, emb)

	priors, err := h.Recall(context.Background(), "some error")
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(priors) != 1 {
		t.Fatalf("priors = %d", len(priors))
	}
	if priors[0].Count != 2 || priors[0].Cause != "nil ptr" {
		t.Fatalf("prior = %+v", priors[0])
	}
	if !priors[0].LastSeen.Equal(time.Date(2026, 8, 5, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("last seen = %v", priors[0].LastSeen)
	}
	if !store.created || store.dim != 3 {
		t.Fatalf("EnsureCollection not honored (created=%v dim=%d)", store.created, store.dim)
	}
}

func TestRecall_Empty(t *testing.T) {
	store := &fakeStore{}
	emb := &fakeEmbedder{vec: []float64{0.1}}
	h := newHist(store, emb)
	priors, err := h.Recall(context.Background(), "x")
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(priors) != 0 {
		t.Fatalf("expected empty, got %+v", priors)
	}
}

func TestRecall_EmbedFails(t *testing.T) {
	store := &fakeStore{}
	emb := &fakeEmbedder{err: errors.New("no model")}
	h := newHist(store, emb)
	if _, err := h.Recall(context.Background(), "x"); err == nil {
		t.Fatal("expected error from embed failure")
	}
}

func TestRecall_EmptyVector(t *testing.T) {
	store := &fakeStore{}
	emb := &fakeEmbedder{vec: []float64{}}
	h := newHist(store, emb)
	if _, err := h.Recall(context.Background(), "x"); err == nil {
		t.Fatal("expected error on empty vector")
	}
}

func TestRecall_SearchFails(t *testing.T) {
	store := &fakeStore{searchErr: errors.New("boom")}
	emb := &fakeEmbedder{vec: []float64{0.1}}
	h := newHist(store, emb)
	if _, err := h.Recall(context.Background(), "x"); err == nil {
		t.Fatal("expected error on search failure")
	}
}

func TestRecord_NewPoint(t *testing.T) {
	store := &fakeStore{}
	emb := &fakeEmbedder{vec: []float64{0.5, 0.6}}
	h := newHist(store, emb)

	embed := []float64{0.5, 0.6}
	if err := h.Record(context.Background(), "sig-A", "rep A", "go", "cause A", "fix A", "qwen2.5:7b", embed); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if store.lastPay == nil {
		t.Fatal("no upsert")
	}
	if store.lastPay["count"] != 1 {
		t.Fatalf("count = %v", store.lastPay["count"])
	}
	if store.lastPay["cause"] != "cause A" {
		t.Fatalf("cause = %v", store.lastPay["cause"])
	}
	if store.lastPay["language"] != "go" {
		t.Fatalf("language = %v", store.lastPay["language"])
	}
	if len(store.lastVec) != 2 || store.lastVec[0] != 0.5 {
		t.Fatalf("vector = %v", store.lastVec)
	}
	if store.lastID != pointID("sig-A") {
		t.Fatalf("id = %d", store.lastID)
	}
}

func TestRecord_ExistingPoint_Increment(t *testing.T) {
	store := &fakeStore{
		points: map[uint64]map[string]any{
			pointID("sig-A"): {
				"count":      float64(2),
				"first_seen": "2026-08-01T00:00:00Z",
			},
		},
	}
	emb := &fakeEmbedder{vec: []float64{0.5, 0.6}}
	h := newHist(store, emb)

	if err := h.Record(context.Background(), "sig-A", "rep A", "go", "cause", "fix", "m", []float64{0.5, 0.6}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if store.lastPay["count"] != 3 {
		t.Fatalf("count = %v, want 3", store.lastPay["count"])
	}
	if store.lastPay["first_seen"] != "2026-08-01T00:00:00Z" {
		t.Fatalf("first_seen not preserved: %v", store.lastPay["first_seen"])
	}
	if ls, _ := store.lastPay["last_seen"].(string); ls == "" {
		t.Fatal("last_seen empty")
	}
}

func TestRecord_ReembedsWhenNil(t *testing.T) {
	store := &fakeStore{}
	emb := &fakeEmbedder{vec: []float64{0.1, 0.2}}
	h := newHist(store, emb)
	if err := h.Record(context.Background(), "sig-B", "rep B", "go", "c", "f", "m", nil); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if emb.last != "rep B" {
		t.Fatalf("did not re-embed representative; last=%q", emb.last)
	}
	if len(store.lastVec) != 2 {
		t.Fatalf("vector = %v", store.lastVec)
	}
}

func TestRecord_EmptySig(t *testing.T) {
	h := newHist(&fakeStore{}, &fakeEmbedder{vec: []float64{0.1}})
	if err := h.Record(context.Background(), "", "rep", "go", "c", "f", "m", []float64{0.1}); err == nil {
		t.Fatal("expected error on empty signature")
	}
}

func TestRecord_GetPointFails(t *testing.T) {
	store := &fakeStore{getErr: errors.New("get boom")}
	h := newHist(store, &fakeEmbedder{vec: []float64{0.1}})
	if err := h.Record(context.Background(), "sig", "rep", "go", "c", "f", "m", []float64{0.1}); err == nil {
		t.Fatal("expected error on GetPoint failure")
	}
}

func TestParseSummary(t *testing.T) {
	content := "WHAT HAPPENED:\nboom\n\nPROBABLE CAUSE:\nthe cause is X\n\nPOTENTIAL FIXES:\n- fix one\n- fix two\n"
	cause, fixes := ParseSummary(content)
	if !strings.Contains(cause, "the cause is X") {
		t.Fatalf("cause = %q", cause)
	}
	if !strings.Contains(fixes, "fix one") {
		t.Fatalf("fixes = %q", fixes)
	}
}

func TestParseSummary_Caps(t *testing.T) {
	long := strings.Repeat("x", 2000)
	content := "PROBABLE CAUSE:\n" + long + "\n\nPOTENTIAL FIXES:\n" + long + "\n"
	cause, fixes := ParseSummary(content)
	// Cap is 1000 bytes + a 3-byte "…" ellipsis; just confirm it was truncated.
	if len(cause) >= 2000 {
		t.Fatalf("cause not capped: len = %d", len(cause))
	}
	if len(fixes) >= 2000 {
		t.Fatalf("fixes not capped: len = %d", len(fixes))
	}
}

func TestFormatBlock_Empty(t *testing.T) {
	if s := FormatBlock(nil); s != "" {
		t.Fatalf("expected empty, got %q", s)
	}
}

func TestFormatBlock_NonEmpty(t *testing.T) {
	priors := []Prior{{Score: 0.92, Count: 3, LastSeen: time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC), Cause: "nil ptr", Fixes: "guard it"}}
	s := FormatBlock(priors)
	if !strings.Contains(s, "PRIOR OCCURRENCES") {
		t.Fatalf("missing header: %q", s)
	}
	if !strings.Contains(s, "similarity 0.92") {
		t.Fatalf("missing score: %q", s)
	}
	if !strings.Contains(s, "seen 3 time(s)") {
		t.Fatalf("missing count: %q", s)
	}
	if !strings.Contains(s, "last 2026-08-05") {
		t.Fatalf("missing date: %q", s)
	}
	if !strings.Contains(s, "PROBABLE CAUSE: nil ptr") {
		t.Fatalf("missing cause: %q", s)
	}
}

func TestEmbedText_Group(t *testing.T) {
	ctx := &analyze.Context{ErrorGroups: []analyze.ErrorGroup{{Representative: "rep-group"}}}
	if got := EmbedText(ctx, "raw-ish"); got != "rep-group" {
		t.Fatalf("got %q", got)
	}
}

func TestEmbedText_RawFallback(t *testing.T) {
	if got := EmbedText(nil, "short raw"); got != "short raw" {
		t.Fatalf("got %q", got)
	}
	long := strings.Repeat("a", 5000)
	if got, want := len(EmbedText(nil, long)), 4096; got != want {
		t.Fatalf("len = %d, want %d", got, want)
	}
}

func TestPointID_Stable(t *testing.T) {
	if pointID("sig") != pointID("sig") {
		t.Fatal("pointID not stable")
	}
	if pointID("sig") == pointID("other") {
		t.Fatal("pointID collides")
	}
}