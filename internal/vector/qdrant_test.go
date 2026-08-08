package vector

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

func TestEnsureCollection_Create(t *testing.T) {
	var createdPath string
	var createdBody []byte
	mux := http.NewServeMux()
	mux.HandleFunc("/collections/errs", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusNotFound) // doesn't exist yet
			return
		}
		if r.Method == http.MethodPut {
			createdPath = r.URL.Path
			body := make(map[string]any)
			json.NewDecoder(r.Body).Decode(&body)
			createdBody, _ = json.Marshal(body)
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"result":true}`))
			return
		}
		w.WriteHeader(http.StatusMethodNotAllowed)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewQdrant(srv.URL, 5*time.Second)
	if err := c.EnsureCollection(context.Background(), "errs", 768); err != nil {
		t.Fatalf("EnsureCollection: %v", err)
	}
	if createdPath != "/collections/errs" {
		t.Fatalf("created path = %s", createdPath)
	}
	var body struct {
		Vectors struct {
			Size     int    `json:"size"`
			Distance string `json:"distance"`
		} `json:"vectors"`
	}
	json.Unmarshal(createdBody, &body)
	if body.Vectors.Size != 768 || body.Vectors.Distance != "Cosine" {
		t.Fatalf("create body = %s", createdBody)
	}
}

func TestEnsureCollection_Exists(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/collections/errs", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			// Collection exists with matching size.
			w.Write([]byte(`{"result":{"config":{"params":{"vectors":{"size":768,"distance":"Cosine"}}}}}`))
			return
		}
		w.WriteHeader(http.StatusMethodNotAllowed)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewQdrant(srv.URL, 5*time.Second)
	if err := c.EnsureCollection(context.Background(), "errs", 768); err != nil {
		t.Fatalf("EnsureCollection on existing: %v", err)
	}
}

func TestEnsureCollection_DimMismatch(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/collections/errs", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"result":{"config":{"params":{"vectors":{"size":128,"distance":"Cosine"}}}}}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewQdrant(srv.URL, 5*time.Second)
	err := c.EnsureCollection(context.Background(), "errs", 768)
	if err == nil {
		t.Fatal("expected dim mismatch error")
	}
	var mm *ErrDimMismatch
	if !errors.As(err, &mm) {
		t.Fatalf("expected ErrDimMismatch, got %T: %v", err, err)
	}
	if mm.Have != 128 || mm.Want != 768 {
		t.Fatalf("mismatch fields = %+v", mm)
	}
}

func TestSearch(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/collections/errs/points/search", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		var b searchBody
		json.NewDecoder(r.Body).Decode(&b)
		if b.Limit != 3 || !b.WithPayload || b.ScoreThreshold != 0.85 {
			t.Errorf("search body = %+v", b)
		}
		if len(b.Vector) != 3 || b.Vector[0] != 0.1 {
			t.Errorf("vector = %v", b.Vector)
		}
		w.Write([]byte(`{"result":[` +
			`{"score":0.92,"payload":{"cause":"nil ptr","count":2},"id":"1"},` +
			`{"score":0.87,"payload":{"cause":"oob","count":1},"id":"2"}` +
			`]}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewQdrant(srv.URL, 5*time.Second)
	matches, err := c.Search(context.Background(), "errs", []float64{0.1, 0.2, 0.3}, 3, 0.85)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(matches) != 2 {
		t.Fatalf("matches = %d", len(matches))
	}
	if matches[0].Score != 0.92 || matches[0].Payload["cause"] != "nil ptr" {
		t.Fatalf("first match = %+v", matches[0])
	}
}

func TestGetPoint_Found(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/collections/errs/points/42", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		// Real Qdrant returns a single point object under "result" (not an array).
		w.Write([]byte(`{"result":{"id":42,"payload":{"count":3,"cause":"x"}}}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewQdrant(srv.URL, 5*time.Second)
	payload, err := c.GetPoint(context.Background(), "errs", 42)
	if err != nil {
		t.Fatalf("GetPoint: %v", err)
	}
	if payload == nil || payload["count"] != float64(3) {
		t.Fatalf("payload = %+v", payload)
	}
}

func TestGetPoint_NotFound(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/collections/errs/points/99", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"status":{"error":"Not found"}}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewQdrant(srv.URL, 5*time.Second)
	payload, err := c.GetPoint(context.Background(), "errs", 99)
	if err != nil {
		t.Fatalf("GetPoint 404 should not error, got %v", err)
	}
	if payload != nil {
		t.Fatalf("expected nil payload, got %+v", payload)
	}
}

func TestGetPoint_ResultNull(t *testing.T) {
	// Some Qdrant responses return {"result":null} for a missing point; treat
	// it as not-found (nil, nil), not an error.
	mux := http.NewServeMux()
	mux.HandleFunc("/collections/errs/points/77", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"result":null,"status":"ok"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewQdrant(srv.URL, 5*time.Second)
	payload, err := c.GetPoint(context.Background(), "errs", 77)
	if err != nil {
		t.Fatalf("GetPoint null result should not error, got %v", err)
	}
	if payload != nil {
		t.Fatalf("expected nil payload, got %+v", payload)
	}
}

func TestUpsert(t *testing.T) {
	var raw []byte
	mux := http.NewServeMux()
	mux.HandleFunc("/collections/errs/points", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("method = %s, want PUT", r.Method)
		}
		if r.URL.Query().Get("wait") != "true" {
			t.Errorf("wait query = %q, want true", r.URL.Query().Get("wait"))
		}
		raw, _ = io.ReadAll(r.Body)
		w.Write([]byte(`{"result":{"operation_id":1,"status":"completed"}}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewQdrant(srv.URL, 5*time.Second)
	err := c.UpsertPoint(context.Background(), "errs", 42, []float64{0.5, 0.6}, map[string]any{"count": 1})
	if err != nil {
		t.Fatalf("UpsertPoint: %v", err)
	}
	var body upsertBody
	json.Unmarshal(raw, &body)
	if len(body.Points) != 1 || body.Points[0].ID != 42 || len(body.Points[0].Vector) != 2 {
		t.Fatalf("upsert body = %s", raw)
	}
	if body.Points[0].Payload["count"] != float64(1) {
		t.Fatalf("payload = %+v", body.Points[0].Payload)
	}
}

func TestUnreachable(t *testing.T) {
	c := NewQdrant("http://127.0.0.1:1", 500*time.Millisecond) // closed port
	_, err := c.Search(context.Background(), "errs", []float64{0.1}, 1, 0.5)
	if !IsUnreachable(err) {
		t.Fatalf("expected ErrUnreachable, got %v", err)
	}
}

func TestIntegration(t *testing.T) {
	if os.Getenv("EXPLAIN_INTEGRATION") != "1" {
		t.Skip("set EXPLAIN_INTEGRATION=1 and run Qdrant on localhost:6333")
	}
	c := NewQdrant("http://localhost:6333", 10*time.Second)
	ctx := context.Background()
	name := "explain-test"
	if err := c.EnsureCollection(ctx, name, 3); err != nil {
		t.Skipf("Qdrant not reachable: %v", err)
	}
	if err := c.UpsertPoint(ctx, name, 1, []float64{0.1, 0.2, 0.3}, map[string]any{"cause": "c1"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	matches, err := c.Search(ctx, name, []float64{0.1, 0.2, 0.3}, 1, 0.5)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(matches) != 1 || matches[0].Payload["cause"] != "c1" {
		t.Fatalf("matches = %+v", matches)
	}
}