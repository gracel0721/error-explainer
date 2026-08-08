package vector

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
)

// QdrantClient implements Store over Qdrant's REST API using net/http.
type QdrantClient struct {
	BaseURL string
	HTTP    *http.Client
}

// --- request/response envelopes ---

// Qdrant wraps most responses in {"result": <...>}. We decode the inner value
// after peeling the envelope; for endpoints whose result is the whole body
// (collection create, upsert) we only check status.
type envelope struct {
	Result json.RawMessage `json:"result"`
}

type createCollectionBody struct {
	Vectors vectorsConfig `json:"vectors"`
}

// vectorsConfig is the named-vector-free form: {"size":N,"distance":"Cosine"}.
type vectorsConfig struct {
	Size     int    `json:"size"`
	Distance string `json:"distance"`
}

type collectionInfo struct {
	Result struct {
		Config struct {
			Params struct {
				Vectors struct {
					Size     int    `json:"size"`
					Distance string `json:"distance"`
				} `json:"vectors"`
			} `json:"params"`
		} `json:"config"`
	} `json:"result"`
}

type searchBody struct {
	Vector        []float64 `json:"vector"`
	Limit         int       `json:"limit"`
	ScoreThreshold float64  `json:"score_threshold"`
	WithPayload   bool      `json:"with_payload"`
}

type searchResult struct {
	Result []struct {
		Score   float64        `json:"score"`
		Payload map[string]any  `json:"payload"`
		ID      any            `json:"id"`
	} `json:"result"`
}

// getPointResult is the GET /collections/{name}/points/{id} envelope. Unlike
// search/scroll (whose result is an array), GET-by-id returns a single point
// object under "result" — or null/404 when the id doesn't exist.
type getPointResult struct {
	Result *pointPayload `json:"result"`
}

type pointPayload struct {
	ID      any            `json:"id"`
	Payload map[string]any `json:"payload"`
}

type upsertBody struct {
	Points []point `json:"points"`
}

type point struct {
	ID      uint64         `json:"id"`
	Vector  []float64      `json:"vector"`
	Payload map[string]any `json:"payload"`
}

// --- helpers ---

func (c *QdrantClient) do(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
	url := c.BaseURL + path
	var br io.Reader
	if body != nil {
		br = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, br)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, &ErrUnreachable{Err: err}
	}
	return resp, nil
}

// errFromStatus parses a Qdrant error body if present and returns a status-coded error.
func errFromStatus(resp *http.Response) error {
	raw, _ := io.ReadAll(resp.Body)
	var e struct {
		Status struct {
			Error string `json:"error"`
		} `json:"status"`
		Time  any    `json:"time"`
	}
	if json.Unmarshal(raw, &e) == nil && e.Status.Error != "" {
		return fmt.Errorf("qdrant returned %d: %s", resp.StatusCode, e.Status.Error)
	}
	return fmt.Errorf("qdrant returned status %d", resp.StatusCode)
}

// --- Store implementation ---

// EnsureCollection creates the collection if it doesn't exist, or verifies the
// existing collection's vector size matches dim.
func (c *QdrantClient) EnsureCollection(ctx context.Context, name string, dim int) error {
	resp, err := c.do(ctx, http.MethodGet, "/collections/"+name, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusOK:
		// Exists: verify dimension.
		var info collectionInfo
		if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
			return fmt.Errorf("decoding collection info: %w", err)
		}
		have := info.Result.Config.Params.Vectors.Size
		if have != dim {
			return &ErrDimMismatch{Collection: name, Have: have, Want: dim}
		}
		return nil
	case resp.StatusCode == http.StatusNotFound:
		// Create it.
		return c.createCollection(ctx, name, dim)
	default:
		return errFromStatus(resp)
	}
}

func (c *QdrantClient) createCollection(ctx context.Context, name string, dim int) error {
	body, err := json.Marshal(createCollectionBody{
		Vectors: vectorsConfig{Size: dim, Distance: "Cosine"},
	})
	if err != nil {
		return err
	}
	resp, err := c.do(ctx, http.MethodPut, "/collections/"+name, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return errFromStatus(resp)
	}
	return nil
}

// Search returns up to limit nearest points to vec with score >= threshold.
func (c *QdrantClient) Search(ctx context.Context, name string, vec []float64, limit int, threshold float64) ([]Match, error) {
	body, err := json.Marshal(searchBody{
		Vector:         vec,
		Limit:          limit,
		ScoreThreshold: threshold,
		WithPayload:     true,
	})
	if err != nil {
		return nil, err
	}
	resp, err := c.do(ctx, http.MethodPost, "/collections/"+name+"/points/search", body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, errFromStatus(resp)
	}
	var sr searchResult
	if err := json.NewDecoder(resp.Body).Decode(&sr); err != nil {
		return nil, fmt.Errorf("decoding qdrant search response: %w", err)
	}
	matches := make([]Match, 0, len(sr.Result))
	for _, r := range sr.Result {
		matches = append(matches, Match{Score: r.Score, Payload: r.Payload})
	}
	return matches, nil
}

// GetPoint returns the payload of point id, or (nil, nil) if not found.
func (c *QdrantClient) GetPoint(ctx context.Context, name string, id uint64) (map[string]any, error) {
	resp, err := c.do(ctx, http.MethodGet, "/collections/"+name+"/points/"+strconv.FormatUint(id, 10), nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, nil
	case resp.StatusCode >= 400:
		return nil, errFromStatus(resp)
	}
	var gpr getPointResult
	if err := json.NewDecoder(resp.Body).Decode(&gpr); err != nil {
		return nil, fmt.Errorf("decoding qdrant get-point response: %w", err)
	}
	if gpr.Result == nil {
		return nil, nil
	}
	return gpr.Result.Payload, nil
}

// UpsertPoint writes (or replaces) the point with id, vec, and payload. wait=true
// makes the upsert block until the index reflects the write, so an immediately
// following Search sees the new point.
func (c *QdrantClient) UpsertPoint(ctx context.Context, name string, id uint64, vec []float64, payload map[string]any) error {
	body, err := json.Marshal(upsertBody{
		Points: []point{{ID: id, Vector: vec, Payload: payload}},
	})
	if err != nil {
		return err
	}
	resp, err := c.do(ctx, http.MethodPut, "/collections/"+name+"/points?wait=true", body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return errFromStatus(resp)
	}
	return nil
}