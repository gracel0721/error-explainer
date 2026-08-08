package ollama

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestEmbed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/embed" {
			t.Errorf("path = %s, want /api/embed", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"embedding":[0.1,0.2,0.3]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, 5*time.Second)
	vec, err := c.Embed(context.Background(), "nomic-embed-text", "hello")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vec) != 3 || vec[0] != 0.1 {
		t.Fatalf("vec = %v", vec)
	}
}

func TestEmbed_PluralEmbeddings(t *testing.T) {
	// Some Ollama versions return "embeddings" (array of arrays) even for a
	// single input; Embed must take the first vector.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"embeddings":[[0.1,0.2,0.3]]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, 5*time.Second)
	vec, err := c.Embed(context.Background(), "nomic-embed-text", "hello")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vec) != 3 || vec[0] != 0.1 {
		t.Fatalf("vec = %v", vec)
	}
}

func TestEmbed_ModelNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":"model 'bogus' not found"}`))
	}))
	defer srv.Close()

	c := New(srv.URL, 5*time.Second)
	_, err := c.Embed(context.Background(), "bogus", "x")
	if !IsModelNotFound(err) {
		t.Fatalf("expected ModelNotFoundError, got %v", err)
	}
}

func TestEmbed_Connection(t *testing.T) {
	c := New("http://127.0.0.1:1", 500*time.Millisecond) // closed port
	_, err := c.Embed(context.Background(), "m", "x")
	if !IsConnection(err) {
		t.Fatalf("expected ConnectionError, got %v", err)
	}
}