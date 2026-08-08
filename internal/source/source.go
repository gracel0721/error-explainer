// Package source extracts relevant source-code snippets from a git repo for
// the parsed stack frames of an error, and formats them for the model prompt.
// It pulls two kinds of context: a window around each frame's file:line, and a
// window around the definition of each function symbol named in the trace.
// Everything is best-effort and byte-capped; failures are silent skips.
package source

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/gvleverett/error-explainer/internal/analyze"
	"github.com/gvleverett/error-explainer/internal/repo"
)

const (
	// DefaultContextLines is the half-window of lines around a frame when none given.
	DefaultContextLines = 10
	// MaxBytes caps the total extracted source sent to the model.
	MaxBytes = 64 * 1024
	// MaxFramesToExtract bounds how many frames we read windows for.
	MaxFramesToExtract = 8
	// MaxSymbols bounds how many symbol definitions we search for.
	MaxSymbols = 5
	// MaxFiles bounds how many distinct files appear in the output.
	MaxFiles = 5
	// symbolContext is the half-window around a definition line.
	symbolContext = 5
)

// Snippet is a window into one file, with one or more marked (offending) lines.
// Lines holds the full file contents; the formatted window is produced lazily
// at finalize time.
type Snippet struct {
	File        string   `json:"file"`
	MarkedLines []int    `json:"marked_lines"`
	Half        int      `json:"-"`          // half-window requested
	Lines       []string `json:"lines"`      // raw file lines (pre-finalize); formatted block after
	StartLine   int      `json:"start_line"` // first line shown in the formatted window
}

// Result is the extraction output.
type Result struct {
	Snippets  []Snippet `json:"snippets"`
	Bytes     int       `json:"bytes"`
	Truncated bool      `json:"truncated"`
}

// Extract reads context windows for the given frames and symbol definitions,
// dedups by file, caps total bytes, and never returns an error. Every miss or
// read failure is a silent skip. Returns an empty Result when repo is nil or
// nothing resolves.
func Extract(r *repo.Repo, ctx *analyze.Context, contextLines int) Result {
	var res Result
	if r == nil || ctx == nil {
		return res
	}
	if contextLines <= 0 {
		contextLines = DefaultContextLines
	}

	byFile := map[string]*Snippet{}
	var order []*Snippet

	// addOrGet records a marked line for file, reading the file on first sight.
	// It returns false when the file can't be read.
	addOrGet := func(file string, line, half int) bool {
		snip, ok := byFile[file]
		if !ok {
			lines, err := r.ReadFileLines(file)
			if err != nil || len(lines) == 0 {
				return false
			}
			snip = &Snippet{File: file, Lines: lines, Half: half}
			byFile[file] = snip
			order = append(order, snip)
		}
		snip.MarkedLines = append(snip.MarkedLines, line)
		// Grow the window if a later marker asks for more context.
		if half > snip.Half {
			snip.Half = half
		}
		return true
	}

	// Frame windows.
	frameCount := 0
	for _, f := range ctx.StackFrames {
		if frameCount >= MaxFramesToExtract {
			break
		}
		path, ok := r.Resolve(f.File)
		if !ok {
			continue
		}
		if addOrGet(path, f.Line, contextLines) {
			frameCount++
		}
	}

	// Symbol definitions.
	symbols := collectSymbols(ctx)
	for _, sym := range symbols {
		if len(order) >= MaxFiles {
			break
		}
		if path, defLine, ok := findDefinition(r, ctx.Language, sym); ok {
			addOrGet(path, defLine, symbolContext)
		}
	}

	return finalize(res, order)
}

// finalize formats each snippet's window, enforces MaxFiles and MaxBytes, and
// marks Truncated when the budget is hit (dropping the overflowing snippet and
// any after it).
func finalize(res Result, order []*Snippet) Result {
	if len(order) > MaxFiles {
		order = order[:MaxFiles]
	}

	formatted := make([]Snippet, 0, len(order))
	bytes := 0
	for _, snip := range order {
		if len(snip.MarkedLines) == 0 || len(snip.Lines) == 0 {
			continue
		}
		block, start := formatSnippet(*snip)
		if bytes+len(block) > MaxBytes {
			res.Truncated = true
			break
		}
		bytes += len(block)
		formatted = append(formatted, Snippet{
			File:        snip.File,
			MarkedLines: snip.MarkedLines,
			StartLine:   start,
			Lines:       []string{block}, // packed formatted block in Lines[0]
		})
	}
	res.Snippets = formatted
	res.Bytes = bytes
	return res
}

// formatSnippet builds the line-numbered window around the union of marked
// lines, expanding by snip.Half on each side. Returns the formatted text and
// the first shown line.
func formatSnippet(snip Snippet) (string, int) {
	marked := make(map[int]bool, len(snip.MarkedLines))
	minLine, maxLine := snip.MarkedLines[0], snip.MarkedLines[0]
	for _, l := range snip.MarkedLines {
		marked[l] = true
		if l < minLine {
			minLine = l
		}
		if l > maxLine {
			maxLine = l
		}
	}
	half := snip.Half
	if half <= 0 {
		half = DefaultContextLines
	}
	start := minLine - half
	if start < 1 {
		start = 1
	}
	end := maxLine + half
	if end > len(snip.Lines) {
		end = len(snip.Lines)
	}

	marks := make([]int, 0, len(marked))
	for l := range marked {
		marks = append(marks, l)
	}
	sort.Ints(marks)

	var b strings.Builder
	fmt.Fprintf(&b, "### %s (lines %d-%d", snip.File, start, end)
	if len(marks) == 1 {
		fmt.Fprintf(&b, "; marked: %d)\n", marks[0])
	} else {
		fmt.Fprintf(&b, "; marked: %s)\n", joinInts(marks))
	}
	for i := start; i <= end; i++ {
		prefix := "  "
		if marked[i] {
			prefix = "> "
		}
		fmt.Fprintf(&b, "%s%4d | %s\n", prefix, i, snip.Lines[i-1])
	}
	return b.String(), start
}

func joinInts(xs []int) string {
	parts := make([]string, len(xs))
	for i, x := range xs {
		parts[i] = fmt.Sprintf("%d", x)
	}
	return strings.Join(parts, ",")
}

// Format renders the snippets as a single plain-text block. Returns "" when
// there are no snippets, so callers omit the prompt section entirely.
func Format(res Result) string {
	if len(res.Snippets) == 0 {
		return ""
	}
	var b strings.Builder
	for _, snip := range res.Snippets {
		if len(snip.Lines) > 0 {
			b.WriteString(snip.Lines[0])
			b.WriteString("\n")
		}
	}
	out := strings.TrimRight(b.String(), "\n")
	if res.Truncated {
		out += "\n\n(source extraction truncated to fit the size budget)"
	}
	return out
}

// ---------------------------------------------------------------------------
// Symbol collection and definition search
// ---------------------------------------------------------------------------

// genericSymbolNames are too common to grep for.
var genericSymbolNames = map[string]bool{
	"main": true, "init": true, "run": true, "handle": true, "do": true,
	"call": true, "start": true, "stop": true, "new": true,
}

// collectSymbols returns unique, non-generic function names from the frames,
// in first-seen order, capped at MaxSymbols.
func collectSymbols(ctx *analyze.Context) []string {
	var out []string
	seen := map[string]bool{}
	for _, f := range ctx.StackFrames {
		name := symbolName(f.Function)
		if name == "" || genericSymbolNames[strings.ToLower(name)] || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
		if len(out) >= MaxSymbols {
			break
		}
	}
	return out
}

// symbolName strips Go receivers and package qualifiers, e.g.
// "main.(*Server).Handle" -> "Handle", "pkg.foo" -> "foo".
func symbolName(fn string) string {
	fn = strings.TrimSpace(fn)
	if fn == "" {
		return ""
	}
	// Go receiver: take the segment after the last ")".
	if strings.HasPrefix(fn, "(") {
		if idx := strings.LastIndexByte(fn, ')'); idx >= 0 {
			rest := strings.TrimSpace(fn[idx+1:])
			if rest == "" {
				// "(*Server)" with nothing after — use the type's last segment.
				inner := fn[1:idx]
				if i := strings.LastIndexByte(inner, '.'); i >= 0 {
					return inner[i+1:]
				}
				return inner
			}
			fn = rest
		}
	}
	// Take the last "." segment.
	if i := strings.LastIndexByte(fn, '.'); i >= 0 {
		fn = fn[i+1:]
	}
	// Strip trailing "()" if present.
	fn = strings.TrimSuffix(fn, "()")
	return fn
}

// defPattern builds an ERE-safe regex that matches a function/method
// definition for sym in the given language. It avoids \s/\b/\w (not portable
// across git grep -E engines) and uses POSIX classes + literal spaces instead.
// Returns the pattern and a git-grep pathspec (file extension), or ""/"" if
// the language is unsupported.
func defPattern(lang analyze.Language, sym string) (pattern, pathspec string) {
	esc := regexp.QuoteMeta(sym)
	// trailing word boundary, ERE-safe: a non-identifier char or end of line.
	const wb = "([^a-zA-Z0-9_]|$)"
	switch lang {
	case analyze.LangGo:
		// "func Resolve(" or "func (recv T) Resolve("
		return `^func ` + esc + wb + `|^func \([^)]*\) ` + esc + wb, "*.go"
	case analyze.LangPython:
		return `^[[:space:]]*def ` + esc + wb, "*.py"
	case analyze.LangRuby:
		return `^[[:space:]]*def ` + esc + wb, "*.rb"
	case analyze.LangRust:
		return `^[[:space:]]*fn ` + esc + wb, "*.rs"
	case analyze.LangNode:
		return `function ` + esc + `\(|` + esc + ` += +\(`, "*.js"
	case analyze.LangJava, analyze.LangDotNet:
		return `^(public|private|protected|static|final|internal|virtual).*` + esc + ` *\(`, "*.java"
	}
	return "", ""
}

// findDefinition greps the repo for a definition of sym and returns the resolved
// file path and line, or false.
func findDefinition(r *repo.Repo, lang analyze.Language, sym string) (string, int, bool) {
	pattern, pathspec := defPattern(lang, sym)
	if pattern == "" {
		return "", 0, false
	}
	hits, err := r.Grep(pattern, pathspec)
	if err != nil || len(hits) == 0 {
		return "", 0, false
	}
	h := hits[0]
	if path, ok := r.Resolve(h.File); ok {
		return path, h.Line, true
	}
	return "", 0, false
}
