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
   ollama pull qwen2.5:7b   # the default analysis model (~4.7 GB)
   ```
   For **historical errors** (on by default) you also need an embedding model and
   the Qdrant vector database:
   ```sh
   ollama pull nomic-embed-text          # embedding model (~270 MB)
   docker run -d -p 6333:6333 --name qdrant qdrant/qdrant   # vector DB
   ```
   History degrades gracefully if either is missing (see [Historical errors](#historical-errors-have-i-seen-this-before)).

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
| `--vector-host` | `http://localhost:6333` | `EXPLAIN_VECTOR_HOST` | Qdrant base URL for error history |
| `--embed-model` | `nomic-embed-text` | `EXPLAIN_EMBED_MODEL` | Ollama model for embeddings |
| `--no-history` | `false` | `EXPLAIN_HISTORY=0` | disable error history (no embed/Qdrant/prior block) |
| `--history-threshold` | `0.85` | `EXPLAIN_HISTORY_THRESHOLD` | min cosine similarity to surface a prior occurrence |
| `--collection` | `explain-errors` | `EXPLAIN_COLLECTION` | Qdrant collection name for error history |
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

### Historical errors (have I seen this before?)

History is **on by default**. Each run embeds the error (Ollama
`nomic-embed-text`), searches a local **Qdrant** collection for similar past
errors, and — when it finds close matches (cosine similarity ≥
`--history-threshold`) — surfaces them to the model as a `PRIOR OCCURRENCES`
block (with the prior PROBABLE CAUSE / POTENTIAL FIXES, occurrence count, and
last-seen date). The model can then note recurrence ("this error has occurred N
times before, last on <date>") and ground its advice in how the error was
resolved last time. After analysis, the error plus the new cause/fixes is
stored back into Qdrant under a deterministic id (FNV-64 of the normalized
signature), bumping an occurrence count if the same error family is seen again.

Everything is best-effort and **never aborts the analysis** — if Qdrant is down
or the embedding model isn't pulled, `explain-error` prints a one-line hint to
stderr and runs without history; exit code is always 0. Disable it entirely
with `--no-history` (or `EXPLAIN_HISTORY=0`) for the classic zero-memory
behavior.

```sh
explain-error -m "panic: runtime error: index out of range [1] with length 0"   # first occurrence → stored
explain-error -m "panic: runtime error: index out of range [3] with length 1"   # near-duplicate → recalled, count++
explain-error --no-history -m "$(cat crash.log)"                                # no history this run
```

To inspect what's been stored:

```sh
curl -s localhost:6333/collections/explain-errors/points/count
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

History failures are non-fatal and only ever print a one-line stderr hint:

- **Qdrant unreachable** → `history disabled: could not reach Qdrant at <host>. Start it with: docker run -p 6333:6333 qdrant/qdrant (or disable with --no-history)`
- **Embedding model not found** → `history disabled: embedding model "<m>" not found. Pull it with: ollama pull <m> (or disable with --no-history)`
- **Ollama down for embeddings** → `history disabled: cannot reach Ollama for embeddings. Is Ollama running? Start it with ollama serve`
- **Collection dimension mismatch** → `history disabled: collection "<name>" has a different vector size than <embed-model>; drop it or change --collection`

## How it works

```
input resolution → analyze (language/frames/groups)
                 → [history] embed → Qdrant recall → PRIOR OCCURRENCES block
                 → prompt (system + user) → POST {OLLAMA_HOST}/api/chat (stream:false)
                 → parse five section markers → colorized sections
                 → [history] parse cause/fixes → Qdrant store (best-effort)
```

The Ollama client uses only the standard library (`net/http`, `encoding/json`) —
no Ollama SDK is required. Qdrant is reached over its REST API with stdlib
`net/http` too (no extra Go module). Both provider boundaries live under
`internal/`, so a cloud backend (e.g. the Claude API) or a different vector store
could be added later without touching the rest.

## Project layout

```
error-explainer/
  go.mod
  main.go
  cmd/
    root.go              # cobra root command, flags, orchestration, spinner
  internal/
    input/input.go       # gather text from -m / file / stdin (+ TTY detection, size cap)
    analyze/analyze.go   # language detection, stack parsing, log grouping, dedup, signature
    repo/repo.go         # git: find root, resolve frame paths, read lines, grep
    source/source.go     # extract frame windows + symbol defs, format, byte-cap
    ollama/client.go     # HTTP client to /api/chat + /api/embed, typed errors
    ollama/model.go      # request/response types
    vector/store.go      # Store interface, Qdrant error types
    vector/qdrant.go     # Qdrant REST client over net/http
    history/history.go   # embed → recall → store orchestrator (have I seen this before?)
    prompt/prompt.go     # system + user messages, fixed section markers
    render/render.go     # parse markers, print colorized sections
```