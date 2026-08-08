package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Client talks to a local Ollama server over its HTTP API using only the
// standard library. No SDK is required because Ollama exposes a simple
// JSON API at POST /api/chat.
type Client struct {
	BaseURL string
	HTTP    *http.Client
}

// New builds a client targeting baseURL with the given request timeout.
func New(baseURL string, timeout time.Duration) *Client {
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		HTTP:    &http.Client{Timeout: timeout},
	}
}

// ConnectionError indicates Ollama could not be reached (not running,
// wrong host, connection refused). Callers map this to friendly guidance.
type ConnectionError struct {
	Err error
}

func (e *ConnectionError) Error() string {
	return fmt.Sprintf("cannot reach Ollama: %v", e.Err)
}

func (e *ConnectionError) Unwrap() error { return e.Err }

// ModelNotFoundError indicates Ollama returned 404 for the requested model.
type ModelNotFoundError struct {
	Model string
}

func (e *ModelNotFoundError) Error() string {
	return fmt.Sprintf("model %q not found", e.Model)
}

// Chat sends the given messages to the model and returns the assistant's
// reply text. It classifies failures into ConnectionError (Ollama down)
// and ModelNotFoundError (HTTP 404) so callers can give targeted advice.
func (c *Client) Chat(ctx context.Context, model string, messages []Message) (string, error) {
	body, err := json.Marshal(ChatRequest{
		Model:    model,
		Stream:   false,
		Messages: messages,
	})
	if err != nil {
		return "", err
	}

	url := c.BaseURL + "/api/chat"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		// Connection refused, DNS failure, timeout, etc. — treat as "Ollama down".
		return "", &ConnectionError{Err: err}
	}
	defer resp.Body.Close()

	var chat ChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&chat); err != nil {
		return "", fmt.Errorf("decoding Ollama response: %w", err)
	}

	switch {
	case resp.StatusCode == http.StatusNotFound:
		return "", &ModelNotFoundError{Model: model}
	case resp.StatusCode >= 400:
		if chat.Error != "" {
			return "", fmt.Errorf("ollama returned %d: %s", resp.StatusCode, chat.Error)
		}
		return "", fmt.Errorf("ollama returned status %d", resp.StatusCode)
	}

	return chat.Message.Content, nil
}

// IsConnection reports whether err is a ConnectionError.
func IsConnection(err error) bool {
	var ce *ConnectionError
	return errors.As(err, &ce)
}

// IsModelNotFound reports whether err is a ModelNotFoundError.
func IsModelNotFound(err error) bool {
	var mnf *ModelNotFoundError
	return errors.As(err, &mnf)
}
