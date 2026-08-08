package ollama

// Message is a single chat message exchanged with Ollama's /api/chat endpoint.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ChatRequest is the body sent to POST /api/chat.
type ChatRequest struct {
	Model    string    `json:"model"`
	Stream   bool      `json:"stream"`
	Messages []Message `json:"messages"`
}

// ChatResponse is the (non-streaming) body returned by /api/chat. The
// model's reply text lives in Message.Content.
type ChatResponse struct {
	Model   string  `json:"model"`
	Message Message `json:"message"`
	Done    bool    `json:"done"`
	// Error is populated by Ollama for some failures (e.g. a bad model).
	Error string `json:"error,omitempty"`
}
