# explain-error

`explain-error` is a command-line tool that takes an error message, stack trace,
or log file and explains it across five dimensions:

- **WHAT HAPPENED** — what the error means
- **PROBABLE CAUSE** — the most likely root cause
- **EVIDENCE** — the specific lines/values from the input that support it
- **INVESTIGATE** — concrete things to check to confirm the cause
- **POTENTIAL FIXES** — actionable fixes, most likely first

Analysis is produced by a **local LLM through [Ollama](https://ollama.com)**, so it runs
fully offline, keeps your error/log data private on your machine, and has no API key
or per-call cost.

## Prerequisites

1. **Go** 1.21+ — `brew install go`
2. **Ollama** installed and running:
   ```sh
   ollama serve        # if not already running
   ollama pull qwen2.5:7b   # the default model (~4.7 GB)
   ```

## Install

```sh
go build -o explain-error .
go install   # optional; note go install names the binary after the module basename
```

`go install` names the binary after the module path's last segment (`error-explainer`).
To get exactly the `explain-error` command on your PATH, build it straight into your
bin directory instead:

```sh
go build -o "$(go env GOPATH)/bin/explain-error" .
```

Then ensure that directory is on your PATH (add to your shell profile):

```sh
export PATH="$PATH:$(go env GOPATH)/bin"
```

## Usage

```sh
explain-error -m "panic: runtime error: index out of range [5] with length 3"
explain-error crash.log                  # analyze a file
go build ./... 2>&1 | explain-error      # analyze piped stderr/stdout
```

### Input precedence

1. `-m` / `--message` flag (literal string)
2. positional argument treated as a **file path**
3. **piped stdin** (detected automatically when stdin is not a TTY)
4. otherwise: print usage and exit non-zero

### Flags

| Flag | Default | Env | Purpose |
|------|---------|-----|---------|
| `-m`, `--message` | — | — | inline error text |
| `--model` | `qwen2.5:7b` | `OLLAMA_MODEL` | Ollama model to use |
| `--host` | `http://localhost:11434` | `OLLAMA_HOST` | Ollama base URL |
| `--timeout` | `120s` | — | HTTP request timeout (local inference can be slow) |
| `--raw` | `false` | — | print the unrendered model output (debug) |
| `--repo` | — | `EXPLAIN_REPO` | path to a git repo to extract relevant source from; empty disables |
| `--context` | `10` | `EXPLAIN_CONTEXT_LINES` | lines of source context around each stack frame |
| `-v`, `--version` | — | — | print the version |

### Source-aware analysis (`--repo`)

Pass `--repo <path>` (often `--repo .`) and `explain-error` lets the model see
the code that produced the error. It:

1. parses the stack trace and detects the language (Go, Python, Java, Node,
   Ruby, Rust, .NET),
2. groups and deduplicates repeated log lines,
3. resolves each `file:line` frame against the repo (handling absolute,
   container-prefixed, and module-path traces), and
4. extracts a line-numbered window around each frame **plus** the definition of
   each function named in the trace (found via `git grep`).

That context is handed to the model as a `PARSED CONTEXT` JSON block plus a
`RELEVANT SOURCE` block, so its EVIDENCE and INVESTIGATE sections cite real
`file:line` references from your code. Everything is best-effort and byte-capped
(64 KB of source, 5 files); missing files, a non-git path, or git not installed
all degrade to "no source block" with at most a stderr warning. Language
detection, grouping, and dedup run even without `--repo` (they self-gate and
add a context block only when the input has parseable structure).

```sh
explain-error --repo . -m "$(cat crash.log)"
go build ./... 2>&1 | explain-error --repo .
```

### Output

By default, sections are printed with colorized headings (cyan, yellow, blue,
magenta, green). Pass `--raw` to see the model's unstructured output for prompt
debugging. If the model emits output that doesn't contain the expected section
markers, the raw response is shown under a neutral heading so the tool never
fails to surface something useful.

## Error handling

- **Ollama not running** → `Is Ollama running? Start it with ollama serve`
- **Model not found** (HTTP 404) → `Model '<model>' not found. Pull it with: ollama pull <model>`
- **Large logs** are capped at **256 KB** and truncated with a warning, to avoid
  blowing the local model's context window.

## How it works

```
input resolution → prompt (system + user) → POST {OLLAMA_HOST}/api/chat (stream:false)
                 → parse five section markers → colorized sections
```

The Ollama client uses only the standard library (`net/http`, `encoding/json`) —
no Ollama SDK is required. The provider boundary lives in `internal/ollama`, so a
cloud backend (e.g. the Claude API) could be added later without touching the rest.

## Project layout

```
error-explainer/
  go.mod
  main.go
  cmd/
    root.go              # cobra root command, flags, orchestration, spinner
  internal/
    input/input.go       # gather text from -m / file / stdin (+ TTY detection, size cap)
    analyze/analyze.go   # language detection, stack parsing, log grouping, dedup
    repo/repo.go         # git: find root, resolve frame paths, read lines, grep
    source/source.go     # extract frame windows + symbol defs, format, byte-cap
    ollama/client.go     # HTTP client to /api/chat, typed errors
    ollama/model.go      # request/response types
    prompt/prompt.go     # system + user messages, fixed section markers
    render/render.go     # parse markers, print colorized sections
```