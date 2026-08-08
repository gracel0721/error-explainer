package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/gvleverett/error-explainer/internal/input"
	"github.com/gvleverett/error-explainer/internal/ollama"
	"github.com/gvleverett/error-explainer/internal/prompt"
	"github.com/gvleverett/error-explainer/internal/render"
	"github.com/spf13/cobra"
)

// Version is set at build time via -ldflags, with a default fallback.
var Version = "0.1.0"

var (
	flagMessage string
	flagModel   string
	flagHost    string
	flagTimeout time.Duration
	flagRaw     bool
)

// envOr returns the env var value when set and non-empty, else def.
func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
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

	messages := []ollama.Message{
		{Role: "system", Content: prompt.System()},
		{Role: "user", Content: prompt.User(res.Text, res.Origin, res.Truncated)},
	}

	client := ollama.New(flagHost, flagTimeout)

	stop := startSpinner()
	content, err := client.Chat(context.Background(), flagModel, messages)
	stop()

	if err != nil {
		return friendlyError(err, flagModel)
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
