package input

import (
	"errors"
	"fmt"
	"io"
	"os"
)

// MaxBytes caps how much input we send to the local model so a large log
// file cannot blow the context window. 256 KB is a comfortable budget for
// a 7B-class model while still covering most stack traces and crash logs.
const MaxBytes = 256 * 1024

// Source records where the error text came from.
type Source int

const (
	SourceFlag  Source = iota // -m / --message literal
	SourceFile                // positional file path
	SourceStdin               // piped stdin
)

// Result is the resolved input to be analyzed.
type Result struct {
	Text      string
	Source    Source
	Origin    string // human-readable origin: "inline", file path, or "stdin"
	Truncated bool   // true if the input exceeded MaxBytes and was cut
}

// ErrNoInput is returned when none of message flag, positional file, or
// piped stdin provided any text. The caller should print usage.
var ErrNoInput = errors.New("no input provided: pass -m, a file path, or pipe via stdin")

// Resolve gathers the error text following a fixed precedence:
//  1. message flag (literal string) if non-empty
//  2. positional arg treated as a file path (error if it can't be read)
//  3. piped stdin (detected via os.Stdin.Stat — ModeCharDevice absent)
//  4. otherwise ErrNoInput
func Resolve(message string, args []string) (*Result, error) {
	if message != "" {
		return &Result{Text: message, Source: SourceFlag, Origin: "inline"}, nil
	}

	if len(args) > 0 {
		path := args[0]
		text, truncated, err := readFileCapped(path)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", path, err)
		}
		return &Result{Text: text, Source: SourceFile, Origin: path, Truncated: truncated}, nil
	}

	// No flag, no positional arg: fall back to stdin only if it's piped.
	stat, err := os.Stdin.Stat()
	if err != nil {
		return nil, fmt.Errorf("checking stdin: %w", err)
	}
	if stat.Mode()&os.ModeCharDevice != 0 {
		// ModeCharDevice set means stdin is a TTY — nothing was piped.
		return nil, ErrNoInput
	}

	text, truncated, err := readReaderCapped(os.Stdin)
	if err != nil {
		return nil, fmt.Errorf("reading stdin: %w", err)
	}
	return &Result{Text: text, Source: SourceStdin, Origin: "stdin", Truncated: truncated}, nil
}

// readFileCapped reads path, truncating at MaxBytes.
func readFileCapped(path string) (string, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", false, err
	}
	defer f.Close()
	return readReaderCapped(f)
}

// readReaderCapped reads up to MaxBytes from r. If more than MaxBytes was
// available, it returns the first MaxBytes and truncated=true.
func readReaderCapped(r io.Reader) (string, bool, error) {
	// Read one byte past the cap so we can detect truncation precisely.
	lr := io.LimitReader(r, MaxBytes+1)
	data, err := io.ReadAll(lr)
	if err != nil {
		return "", false, err
	}
	truncated := len(data) > MaxBytes
	if truncated {
		data = data[:MaxBytes]
	}
	return string(data), truncated, nil
}
