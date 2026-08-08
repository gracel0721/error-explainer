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
| `-v`, `--version` | — | — | print the version |

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
    ollama/client.go     # HTTP client to /api/chat, typed errors
    ollama/model.go      # request/response types
    prompt/prompt.go     # system + user messages, fixed section markers
    render/render.go     # parse markers, print colorized sections
```