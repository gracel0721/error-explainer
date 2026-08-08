package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gvleverett/error-explainer/internal/analyze"
	"github.com/gvleverett/error-explainer/internal/history"
	"github.com/gvleverett/error-explainer/internal/input"
	"github.com/gvleverett/error-explainer/internal/ollama"
	"github.com/gvleverett/error-explainer/internal/prompt"
	"github.com/gvleverett/error-explainer/internal/render"
	"github.com/gvleverett/error-explainer/internal/repo"
	"github.com/gvleverett/error-explainer/internal/source"
	"github.com/gvleverett/error-explainer/internal/vector"
	"github.com/spf13/cobra"
)

// Version is set at build time via -ldflags, with a default fallback.
var Version = "0.1.0"

var (
	flagMessage      string
	flagModel        string
	flagHost         string
	flagTimeout      time.Duration
	flagRaw          bool
	flagRepo         string
	flagContext      int
	flagVectorHost   string
	flagEmbedModel   string
	flagNoHistory    bool
	flagHistThreshold float64
	flagCollection   string
)

// envOr returns the env var value when set and non-empty, else def.
func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// envOrInt returns the env var int value when set and parseable, else def.
func envOrInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

// envOrBool returns the env var bool value when set ("1"/"true"/"yes" → true,
// "0"/"false"/"no" → false), else def.
func envOrBool(key string, def bool) bool {
	if v := os.Getenv(key); v != "" {
		switch strings.ToLower(v) {
		case "1", "true", "yes":
			return true
		case "0", "false", "no":
			return false
		}
	}
	return def
}

// envOrFloat64 returns the env var float value when set and parseable, else def.
func envOrFloat64(key string, def float64) float64 {
	if v := os.Getenv(key); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return def
}

var rootCmd = &cobra.Command{
	Use:   "explain-error [-m \"<error text>\" | <file> | < piped input]",
	Short: "Explain an error, stack trace, or log using a local LLM (Ollama)",
	Long: `explain-error analyzes an error message, stack trace, or log file and explains it
across five sections: what happened, probable cause, evidence, what to investigate,
and potential fixes. Analysis runs through a local Ollama model, so error data stays
on your machine with no API key or per-call cost.

Input precedence: -m/--message flag, then a positional file path, then piped stdin.`,
	Args: cobra.MaximumNArgs(1),
	RunE: run,
}

func init() {
	rootCmd.Flags().StringVarP(&flagMessage, "message", "m", "", "inline error text to analyze")
	rootCmd.Flags().StringVar(&flagModel, "model", envOr("OLLAMA_MODEL", "qwen2.5:7b"), "Ollama model to use (env: OLLAMA_MODEL)")
	rootCmd.Flags().StringVar(&flagHost, "host", envOr("OLLAMA_HOST", "http://localhost:11434"), "Ollama base URL (env: OLLAMA_HOST)")
	rootCmd.Flags().DurationVar(&flagTimeout, "timeout", 120*time.Second, "HTTP request timeout for Ollama")
	rootCmd.Flags().BoolVar(&flagRaw, "raw", false, "print the unrendered model output (debug)")
	rootCmd.Flags().StringVar(&flagRepo, "repo", envOr("EXPLAIN_REPO", ""), "path to a git repo to extract relevant source from (env: EXPLAIN_REPO); empty disables extraction")
	rootCmd.Flags().IntVar(&flagContext, "context", envOrInt("EXPLAIN_CONTEXT_LINES", 10), "lines of source context around each stack frame")

	// History (have-I-seen-this-before) flags. On by default; disable with
	// --no-history or EXPLAIN_HISTORY=0. History failures never abort the
	// analysis — they print a one-line hint to stderr and continue.
	rootCmd.Flags().StringVar(&flagVectorHost, "vector-host", envOr("EXPLAIN_VECTOR_HOST", "http://localhost:6333"), "Qdrant base URL for error history (env: EXPLAIN_VECTOR_HOST)")
	rootCmd.Flags().StringVar(&flagEmbedModel, "embed-model", envOr("EXPLAIN_EMBED_MODEL", "nomic-embed-text"), "Ollama model for embeddings (env: EXPLAIN_EMBED_MODEL)")
	rootCmd.Flags().BoolVar(&flagNoHistory, "no-history", false, "disable error history (no embedding, Qdrant, or prior-occurrences block)")
	rootCmd.Flags().Float64Var(&flagHistThreshold, "history-threshold", envOrFloat64("EXPLAIN_HISTORY_THRESHOLD", 0.85), "minimum cosine similarity to surface a prior occurrence")
	rootCmd.Flags().StringVar(&flagCollection, "collection", envOr("EXPLAIN_COLLECTION", "explain-errors"), "Qdrant collection name for error history (env: EXPLAIN_COLLECTION)")

	// Setting Version makes cobra register and handle --version automatically.
	rootCmd.Version = Version
	rootCmd.SetVersionTemplate("explain-error version " + Version + "\n")
}

// Execute is the entry point called from main.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func run(cmd *cobra.Command, args []string) error {
	res, err := input.Resolve(flagMessage, args)
	if err != nil {
		if err == input.ErrNoInput {
			return fmt.Errorf("no input: use -m \"<text>\", pass a file path, or pipe input via stdin")
		}
		return err
	}

	if res.Truncated {
		fmt.Fprintf(os.Stderr, "warning: input exceeded %d bytes and was truncated\n", input.MaxBytes)
	}

	// Analyze the input for language, stack frames, and deduplicated error
	// groups. This is always-on but self-gating: ctx.JSON() returns "" when
	// nothing useful is found, so simple inputs add no prompt block.
	ctx := analyze.Analyze(res.Text)
	contextBlock := ctx.JSON()

	// Source extraction is gated behind --repo. It uses the parsed frames to
	// read windows around file:line plus symbol definitions found via git grep.
	var sourceBlock string
	if flagRepo != "" {
		if r, rerr := repo.Find(flagRepo); rerr != nil {
			fmt.Fprintf(os.Stderr, "warning: --repo disabled (%v); continuing without source extraction\n", rerr)
		} else {
			sourceBlock = source.Format(source.Extract(r, ctx, flagContext))
		}
	}

	// History ("have I seen this before?") is on by default but fully
	// best-effort: a recall failure prints a one-line hint and disables history
	// for this run (including the store step). The analysis always runs and
	// exits 0 regardless. --no-history short-circuits the whole path.
	historyEnabled := !flagNoHistory && envOrBool("EXPLAIN_HISTORY", true)
	var priorBlock string
	var hist *history.History
	var embed []float64
	if historyEnabled {
		hist = &history.History{
			Store:      vector.NewQdrant(flagVectorHost, flagTimeout),
			Embedder:   ollama.New(flagHost, flagTimeout),
			EmbedModel: flagEmbedModel,
			Collection: flagCollection,
			Threshold:  flagHistThreshold,
			TopK:       3,
		}
		embedText := history.EmbedText(ctx, res.Text)
		priors, herr := hist.Recall(context.Background(), embedText)
		if herr != nil {
			fmt.Fprintln(os.Stderr, historyHint(herr, flagEmbedModel, flagVectorHost, flagCollection))
			hist = nil // skip store too
		} else {
			priorBlock = history.FormatBlock(priors)
		}
	}

	messages := []ollama.Message{
		{Role: "system", Content: prompt.System()},
		{Role: "user", Content: prompt.User(res.Text, res.Origin, res.Truncated, contextBlock, sourceBlock, priorBlock)},
	}

	client := ollama.New(flagHost, flagTimeout)

	stop := startSpinner()
	content, err := client.Chat(context.Background(), flagModel, messages)
	stop()

	if err != nil {
		return friendlyError(err, flagModel)
	}

	// Best-effort store: record this error + its analysis so future matches can
	// recall the resolution. Reuses the recall-time embedding when available to
	// avoid a second embed call. Never aborts the run.
	if hist != nil && err == nil {
		sig, rep := analyze.Signature(res.Text)
		cause, fixes := history.ParseSummary(content)
		if serr := hist.Record(context.Background(), sig, rep, string(ctx.Language), cause, fixes, flagModel, embed); serr != nil {
			fmt.Fprintln(os.Stderr, historyHint(serr, flagEmbedModel, flagVectorHost, flagCollection))
		}
	}

	if flagRaw {
		fmt.Print(content)
		if !strings.HasSuffix(content, "\n") {
			fmt.Println()
		}
		return nil
	}

	fmt.Println(render.Render(content))
	return nil
}

// friendlyError turns low-level Ollama failures into actionable guidance.
func friendlyError(err error, model string) error {
	switch {
	case ollama.IsConnection(err):
		return fmt.Errorf("could not connect to Ollama at %s.\nIs Ollama running? Start it with `ollama serve`", flagHost)
	case ollama.IsModelNotFound(err):
		return fmt.Errorf("model %q not found.\nPull it with: `ollama pull %s`", model, model)
	default:
		return err
	}
}

// historyHint maps a history-path failure to a one-line, actionable stderr hint
// and returns it. History failures never abort the run — the caller prints this
// hint and continues with analysis. The message always ends with how to
// disable history, mirroring friendlyError's tone.
func historyHint(err error, embedModel, vectorHost, collection string) string {
	switch {
	case ollama.IsConnection(err):
		return "history disabled: cannot reach Ollama for embeddings. Is Ollama running? Start it with `ollama serve` (or disable with --no-history / EXPLAIN_HISTORY=0)"
	case ollama.IsModelNotFound(err):
		return fmt.Sprintf("history disabled: embedding model %q not found. Pull it with: `ollama pull %s` (or disable with --no-history)", embedModel, embedModel)
	case vector.IsUnreachable(err):
		return fmt.Sprintf("history disabled: could not reach Qdrant at %s. Start it with: `docker run -p 6333:6333 qdrant/qdrant` (or disable with --no-history / EXPLAIN_HISTORY=0)", vectorHost)
	}
	var mm *vector.ErrDimMismatch
	if errors.As(err, &mm) {
		return fmt.Sprintf("history disabled: collection %q has a different vector size than %s; drop it or change --collection (or disable with --no-history)", collection, embedModel)
	}
	// Store-step-only failures get a softer message: the analysis was shown.
	return fmt.Sprintf("history store skipped: %v (analysis was still shown above; disable with --no-history)", err)
}

// startSpinner prints an animated "Analyzing…" indicator to stderr while we
// wait on local inference. It returns a stop function that clears the line.
// When stderr is not a TTY (e.g. piped into another command), it prints a
// single static line instead so it does not pollute captured output.
func startSpinner() func() {
	if !isTerminal(os.Stderr) {
		fmt.Fprintln(os.Stderr, "Analyzing…")
		return func() {}
	}

	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
		i := 0
		for {
			select {
			case <-stop:
				// Clear the spinner line.
				fmt.Fprintf(os.Stderr, "\r\033[K")
				return
			default:
				fmt.Fprintf(os.Stderr, "\r%s Analyzing…", frames[i%len(frames)])
				i++
				time.Sleep(80 * time.Millisecond)
			}
		}
	}()

	return func() {
		close(stop)
		<-done
	}
}

// isTerminal reports whether f is connected to a terminal.
func isTerminal(f *os.File) bool {
	stat, err := f.Stat()
	if err != nil {
		return false
	}
	return (stat.Mode() & os.ModeCharDevice) != 0
}
