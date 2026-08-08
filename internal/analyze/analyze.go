// Package analyze performs pure text analysis of an error/log input: language
// detection, stack-trace parsing, and log grouping with deduplication. It does
// no I/O and is stdlib-only, so it is straightforward to unit-test.
package analyze

import (
	"encoding/json"
	"regexp"
	"sort"
	"strings"
)

// Language is the detected source language of the input.
type Language string

const (
	LangGo      Language = "go"
	LangPython  Language = "python"
	LangJava    Language = "java"
	LangNode    Language = "node"
	LangRuby    Language = "ruby"
	LangRust    Language = "rust"
	LangDotNet  Language = "dotnet"
	LangGeneric Language = "generic"
)

// Frame is one stack-trace location. Function is the raw symbol from the trace
// (the source package strips receivers/package qualifiers for symbol search).
type Frame struct {
	File     string `json:"file"`
	Line     int    `json:"line"`
	Function string `json:"function,omitempty"`
	Module   string `json:"module,omitempty"`
}

// ErrorGroup is a collapsed set of near-duplicate log blocks.
type ErrorGroup struct {
	Signature      string `json:"signature"`
	Count          int    `json:"count"`
	Representative string `json:"representative"`
}

// Context is the structured analysis of the input text. It is serialized to a
// compact JSON block for the model prompt.
type Context struct {
	Language    Language     `json:"language"`
	Framework   string       `json:"framework,omitempty"`
	StackFrames []Frame      `json:"stack_frames,omitempty"`
	ErrorGroups []ErrorGroup `json:"error_groups,omitempty"`
}

const (
	MaxFrames = 25
	MaxGroups = 20
)

// Analyze runs the full pipeline and always returns a non-nil Context.
func Analyze(text string) *Context {
	lang := DetectLanguage(text)
	return &Context{
		Language:    lang,
		StackFrames: ParseStack(text, lang),
		ErrorGroups: GroupErrors(text),
	}
}

// JSON returns the compact serialization of the context, or "" when there is
// nothing useful (generic language, no frames, no groups) so callers can omit
// the prompt block entirely.
func (c *Context) JSON() string {
	if c == nil {
		return ""
	}
	if c.Language == LangGeneric && len(c.StackFrames) == 0 && len(c.ErrorGroups) == 0 {
		return ""
	}
	b, err := json.Marshal(c)
	if err != nil {
		return ""
	}
	return string(b)
}

// ---------------------------------------------------------------------------
// Language detection
// ---------------------------------------------------------------------------

// langSignal is a regex whose presence scores one point for a language.
type langSignal struct {
	lang Language
	re   *regexp.Regexp
}

var langSignals = []langSignal{
	{LangGo, regexp.MustCompile(`\bpanic:\s`)},
	{LangGo, regexp.MustCompile(`\bgoroutine\s+\d+\s`)},
	{LangGo, regexp.MustCompile(`\[recovered\]`)},
	{LangGo, regexp.MustCompile(`/usr/local/go/src/`)},
	{LangGo, regexp.MustCompile(`\bcreated by `)},
	{LangGo, regexp.MustCompile(`\.go:\d+`)},
	{LangPython, regexp.MustCompile(`Traceback \(most recent call last\):`)},
	{LangPython, regexp.MustCompile(`(?m)^\s*File ".*", line \d+, in`)},
	{LangPython, regexp.MustCompile(`(?m)^\s*\w+Error:`)},
	{LangPython, regexp.MustCompile(`\bError:\s`)},
	{LangJava, regexp.MustCompile(`Exception in thread`)},
	{LangJava, regexp.MustCompile(`(?m)^\s*at [\w.$]+\(`)},
	{LangJava, regexp.MustCompile(`Caused by:`)},
	{LangJava, regexp.MustCompile(`\.java:\d+\)`)},
	{LangNode, regexp.MustCompile(`(?m)^\s*at .+:\d+:\d+`)},
	{LangNode, regexp.MustCompile(`node:internal/`)},
	{LangNode, regexp.MustCompile(`\bthrow err;`)},
	{LangNode, regexp.MustCompile(`(?m)^\s*at .* \(.*:\d+:\d+\)`)},
	{LangRuby, regexp.MustCompile(`\.rb:\d+:in `)},
	{LangRuby, regexp.MustCompile(`(?m)^.*:\d+:in ['"]`)},
	{LangRust, regexp.MustCompile(`panicked at `)},
	{LangRust, regexp.MustCompile(`stack backtrace:`)},
	{LangRust, regexp.MustCompile(`\.rs:\d+:\d+`)},
	{LangDotNet, regexp.MustCompile(`(?m)^\s*at .* in .*:line \d+`)},
	{LangDotNet, regexp.MustCompile(`Unhandled exception`)},
	{LangDotNet, regexp.MustCompile(`System\.\w+Exception`)},
}

// langPriority breaks detection ties (earlier = preferred).
var langPriority = []Language{
	LangGo, LangPython, LangJava, LangRust, LangRuby, LangNode, LangDotNet,
}

// DetectLanguage scores the text against per-language signals and returns the
// best match, or LangGeneric when nothing scores.
func DetectLanguage(text string) Language {
	scores := map[Language]int{}
	for _, s := range langSignals {
		if s.re.MatchString(text) {
			scores[s.lang]++
		}
	}
	best := LangGeneric
	bestScore := 0
	for _, lang := range langPriority {
		if sc := scores[lang]; sc > bestScore {
			best = lang
			bestScore = sc
		}
	}
	return best
}

// ---------------------------------------------------------------------------
// Stack-trace parsing
// ---------------------------------------------------------------------------

// goFuncLine matches a Go call line like "main.run()" or "pkg.(*Type).Method(...)".
var goFuncLine = regexp.MustCompile(`^([A-Za-z0-9_./\-(*)"'\]]+)\(.*\)\s*$`)

// goFrameLine matches the indented "  /path/file.go:42 +0x1a" line.
var goFrameLine = regexp.MustCompile(`^\t(.+\.go):(\d+)(?:\s.*)?$`)

// goGoroutine matches a goroutine header, used as a pseudo-marker (not a frame).
var goGoroutine = regexp.MustCompile(`^goroutine \d+ `)

var (
	pyFrame      = regexp.MustCompile(`^\s*File "(.+)", line (\d+), in (.+)`)
	jaFrame      = regexp.MustCompile(`^\s*at (?:([\w.$]+)\.)?([\w$<>]+)\(([\w$]+\.java):(\d+)\)`)
	noFrameA     = regexp.MustCompile(`^\s*at (.+) \((.+):(\d+):(\d+)\)`)
	noFrameB     = regexp.MustCompile(`^\s*at (.+):(\d+):(\d+)`)
	rbFrame      = regexp.MustCompile(`^(.+):(\d+):in ['"](.+)`)
	ruFrame      = regexp.MustCompile(`^(.+\.rs):(\d+):\d+`)
	ruPanic      = regexp.MustCompile(`panicked at(?:.*?\s)?(.+\.rs):(\d+):(\d+)`)
	dnFrame      = regexp.MustCompile(`^\s*at (.+) in (.+):line (\d+)`)
	genericFrame = regexp.MustCompile(`(\S+\.[A-Za-z][A-Za-z0-9]*):(\d+)`)
)

// ParseStack extracts frames for the detected language, in source order, capped
// at MaxFrames. Falls back to a generic file:line sweep when a language yields
// nothing.
func ParseStack(text string, lang Language) []Frame {
	var frames []Frame
	switch lang {
	case LangGo:
		frames = parseGo(text)
	case LangPython:
		frames = parseRegexp(text, pyFrame, 1, 2, 3, "")
	case LangJava:
		frames = parseJava(text)
	case LangNode:
		frames = parseNode(text)
	case LangRuby:
		frames = parseRegexp(text, rbFrame, 1, 2, 3, "")
	case LangRust:
		frames = parseRust(text)
	case LangDotNet:
		frames = parseRegexp(text, dnFrame, 2, 3, 1, "")
	default:
		frames = parseGeneric(text)
	}
	if len(frames) == 0 && lang != LangGeneric {
		frames = parseGeneric(text)
	}
	if len(frames) > MaxFrames {
		frames = frames[:MaxFrames]
	}
	return frames
}

func parseGo(text string) []Frame {
	var frames []Frame
	var pendingFunc string
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if goGoroutine.MatchString(line) {
			continue
		}
		if m := goFrameLine.FindStringSubmatch(line); m != nil {
			frames = append(frames, Frame{
				File:     m[1],
				Line:     atoi(m[2]),
				Function: pendingFunc,
			})
			pendingFunc = ""
			continue
		}
		// A non-indented call line becomes the pending function for the next frame.
		if goFuncLine.MatchString(line) {
			pendingFunc = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(line), "()"))
			// Keep only the symbol portion (trim trailing "()" already done).
		}
	}
	return frames
}

// parseRegexp is a generic extractor for single-line frame regexes with groups
// (fileG, lineG, funcG). moduleG empty means no module.
func parseRegexp(text string, re *regexp.Regexp, fileG, lineG, funcG int, _ string) []Frame {
	var frames []Frame
	for _, line := range strings.Split(text, "\n") {
		m := re.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		f := Frame{File: m[fileG], Line: atoi(m[lineG])}
		if funcG > 0 && funcG < len(m) {
			f.Function = m[funcG]
		}
		frames = append(frames, f)
	}
	return frames
}

func parseJava(text string) []Frame {
	var frames []Frame
	for _, line := range strings.Split(text, "\n") {
		m := jaFrame.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		f := Frame{Function: m[2], File: m[3], Line: atoi(m[4])}
		if m[1] != "" {
			f.Module = m[1]
		}
		frames = append(frames, f)
	}
	return frames
}

func parseNode(text string) []Frame {
	var frames []Frame
	for _, line := range strings.Split(text, "\n") {
		if m := noFrameA.FindStringSubmatch(line); m != nil {
			frames = append(frames, Frame{Function: m[1], File: m[2], Line: atoi(m[3])})
			continue
		}
		if m := noFrameB.FindStringSubmatch(line); m != nil {
			frames = append(frames, Frame{Function: "", File: m[1], Line: atoi(m[2])})
		}
	}
	return frames
}

func parseRust(text string) []Frame {
	var frames []Frame
	if m := ruPanic.FindStringSubmatch(strings.SplitN(text, "\n", 2)[0]); m != nil {
		frames = append(frames, Frame{File: m[1], Line: atoi(m[2])})
	}
	frames = append(frames, parseRegexp(text, ruFrame, 1, 2, 0, "")...)
	return frames
}

func parseGeneric(text string) []Frame {
	var frames []Frame
	seen := map[string]bool{}
	for _, line := range strings.Split(text, "\n") {
		for _, m := range genericFrame.FindAllStringSubmatch(line, -1) {
			key := m[1] + ":" + m[2]
			if seen[key] {
				continue
			}
			seen[key] = true
			frames = append(frames, Frame{File: m[1], Line: atoi(m[2])})
		}
	}
	return frames
}

// ---------------------------------------------------------------------------
// Log grouping & deduplication
// ---------------------------------------------------------------------------

var (
	tsPrefix    = regexp.MustCompile(`(?m)^\s*\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}(\.\d+)?Z?\s*`)
	shortTS     = regexp.MustCompile(`(?m)^\s*\d{2}:\d{2}:\d{2}\s*`)
	hexAddr     = regexp.MustCompile(`0x[0-9a-fA-F]+`)
	pidBracket  = regexp.MustCompile(`\[\d+\]`)
	pidEq       = regexp.MustCompile(`\bpid=\d+`)
	fileLineNum = regexp.MustCompile(`\.([A-Za-z][A-Za-z0-9]*):(\d+)`)
	goroutineN  = regexp.MustCompile(`goroutine \d+`)
	blockStart  = regexp.MustCompile(`^(?:\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}|\d{2}:\d{2}:\d{2}|ERROR|WARN|panic:|Traceback|FATAL|Exception|goroutine \d+|Caused by:)`)
)

// GroupErrors splits the text into error blocks and collapses near-duplicates
// into ErrorGroups (one representative + count), capped at MaxGroups.
func GroupErrors(text string) []ErrorGroup {
	blocks := splitBlocks(text)
	groups := map[string]*ErrorGroup{}
	var order []string
	for _, b := range blocks {
		rep := strings.TrimSpace(b)
		if rep == "" {
			continue
		}
		sig := normalize(rep)
		if sig == "" {
			continue
		}
		if g, ok := groups[sig]; ok {
			g.Count++
		} else {
			if len(rep) > 500 {
				rep = rep[:500] + "…"
			}
			groups[sig] = &ErrorGroup{Signature: sig, Count: 1, Representative: rep}
			order = append(order, sig)
		}
	}

	out := make([]ErrorGroup, 0, len(order))
	for _, sig := range order {
		out = append(out, *groups[sig])
	}
	// Drop trivial single-line noise (short, count==1).
	filtered := out[:0]
	for _, g := range out {
		if g.Count == 1 && len(strings.TrimSpace(g.Representative)) < 8 {
			continue
		}
		filtered = append(filtered, g)
	}
	out = filtered
	// Sort by count desc, stable on first-seen order for ties.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return indexof(order, out[i].Signature) < indexof(order, out[j].Signature)
	})
	if len(out) > MaxGroups {
		out = out[:MaxGroups]
	}
	return out
}

func indexof(s []string, v string) int {
	for i, x := range s {
		if x == v {
			return i
		}
	}
	return -1
}

// splitBlocks segments the text into blocks. A blank line ends a block; a line
// matching a block-start marker begins a new block even mid-text (so each
// timestamped log entry is its own block).
func splitBlocks(text string) []string {
	var blocks []string
	var cur strings.Builder
	hasCur := false
	flush := func() {
		if hasCur {
			blocks = append(blocks, cur.String())
			cur.Reset()
			hasCur = false
		}
	}
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) == "" {
			flush()
			continue
		}
		if hasCur && blockStart.MatchString(line) {
			flush()
		}
		if hasCur {
			cur.WriteByte('\n')
		}
		cur.WriteString(line)
		hasCur = true
	}
	flush()
	return blocks
}

// normalize reduces a block to a signature by stripping volatile details so
// near-duplicate lines collapse to the same key.
func normalize(block string) string {
	s := tsPrefix.ReplaceAllString(block, "")
	s = shortTS.ReplaceAllString(s, "")
	s = hexAddr.ReplaceAllString(s, "0xADDR")
	s = pidBracket.ReplaceAllString(s, "[PID]")
	s = pidEq.ReplaceAllString(s, "pid=N")
	s = goroutineN.ReplaceAllString(s, "goroutine N")
	s = fileLineNum.ReplaceAllString(s, ".${1}:N")
	// Collapse whitespace.
	s = strings.Join(strings.Fields(s), " ")
	return strings.TrimSpace(s)
}

// Signature returns a deterministic collision key for the input (sig) and a
// short representative string (rep) suitable for embedding/storage. When
// GroupErrors finds at least one group, it uses the top group's normalized
// Signature and capped Representative; otherwise it falls back to normalizing
// the first 4KB of raw text for the signature and the first 500 chars for the
// representative. This gives history a stable key without duplicating the
// normalization logic.
func Signature(text string) (sig, rep string) {
	groups := GroupErrors(text)
	if len(groups) > 0 {
		return groups[0].Signature, groups[0].Representative
	}
	head := text
	if len(head) > 4096 {
		head = head[:4096]
	}
	rep = text
	if len(rep) > 500 {
		rep = rep[:500]
	}
	return normalize(head), rep
}

// atoi is a panic-free integer parse (returns 0 on failure).
func atoi(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return n
		}
		n = n*10 + int(r-'0')
	}
	return n
}
