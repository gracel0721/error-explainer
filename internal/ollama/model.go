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

// EmbedRequest is the body sent to POST /api/embed.
type EmbedRequest struct {
	Model string `json:"model"`
	Input string `json:"input"`
}

// EmbedResponse is the body returned by /api/embed. Ollama returns the vector
// for a single input under "embedding", but some versions return "embeddings"
// (an array of arrays) even for one input, so we decode both and let Embed
// pick whichever is present.
type EmbedResponse struct {
	Embedding  []float64   `json:"embedding"`
	Embeddings [][]float64 `json:"embeddings"`
	Error      string      `json:"error,omitempty"`
}
