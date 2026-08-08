package source

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gvleverett/error-explainer/internal/analyze"
	"github.com/gvleverett/error-explainer/internal/repo"
)

// buildRepo creates a temp git repo with a Go file whose Resolve returns a
// path with many lines, so we can test windowing.
func buildRepo(t *testing.T) (*repo.Repo, string) {
	t.Helper()
	root := t.TempDir()
	var b strings.Builder
	b.WriteString("package cmd\n")
	b.WriteString("// Resolve gathers input.\n")
	for i := 3; i <= 40; i++ {
		b.WriteString("line content\n")
	}
	// line 43 = "func Resolve(...)"; mark symbol def there.
	b.WriteString("func Resolve(message string) (*Result, error) {\n")
	b.WriteString("	return nil, nil\n")
	b.WriteString("}\n")
	path := filepath.Join("cmd", "root.go")
	mustWrite2(t, filepath.Join(root, path), b.String())
	mustGit2(t, root, "init")
	mustGit2(t, root, "add", "-A")
	mustGit2(t, root, "commit", "-m", "init")
	r, err := repo.Find(root)
	if err != nil {
		t.Fatal(err)
	}
	return r, path
}

func mustWrite2(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustGit2(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
	)
	if err := cmd.Run(); err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
}

func TestExtract_FrameWindow(t *testing.T) {
	r, _ := buildRepo(t)
	ctx := &analyze.Context{Language: analyze.LangGo, StackFrames: []analyze.Frame{
		{File: "cmd/root.go", Line: 10, Function: "main.run"},
	}}
	res := Extract(r, ctx, 5)
	if len(res.Snippets) != 1 {
		t.Fatalf("snippets = %d, want 1", len(res.Snippets))
	}
	block := res.Snippets[0].Lines[0]
	// Window around line 10 with half=5 => lines 5-15.
	if !strings.Contains(block, "lines 5-15") {
		t.Fatalf("window not 5-15:\n%s", block)
	}
	marked := "> " + fmt.Sprintf("%4d", 10) + " |"
	if !strings.Contains(block, marked) {
		t.Fatalf("line 10 not marked (wanted %q):\n%s", marked, block)
	}
}

func TestExtract_DedupByFile(t *testing.T) {
	r, _ := buildRepo(t)
	ctx := &analyze.Context{Language: analyze.LangGo, StackFrames: []analyze.Frame{
		{File: "cmd/root.go", Line: 8, Function: "a"},
		{File: "cmd/root.go", Line: 12, Function: "b"},
	}}
	res := Extract(r, ctx, 5)
	if len(res.Snippets) != 1 {
		t.Fatalf("snippets = %d, want 1 (deduped by file)", len(res.Snippets))
	}
	// Window should span both marks: min 8 - 5 = 3, max 12 + 5 = 17.
	block := res.Snippets[0].Lines[0]
	if !strings.Contains(block, "lines 3-17") {
		t.Fatalf("window not 3-17:\n%s", block)
	}
	if !strings.Contains(block, "marked: 8,12") {
		t.Fatalf("both marks not listed:\n%s", block)
	}
}

func TestExtract_MissingFileSkipped(t *testing.T) {
	r, _ := buildRepo(t)
	ctx := &analyze.Context{Language: analyze.LangGo, StackFrames: []analyze.Frame{
		{File: "nope.go", Line: 5, Function: "x"},
	}}
	res := Extract(r, ctx, 5)
	if len(res.Snippets) != 0 {
		t.Fatalf("expected no snippets, got %d", len(res.Snippets))
	}
}

func TestExtract_SymbolSearch(t *testing.T) {
	r, _ := buildRepo(t)
	// "Resolve" is non-generic and defined at line 43.
	ctx := &analyze.Context{Language: analyze.LangGo, StackFrames: []analyze.Frame{
		{File: "cmd/root.go", Line: 10, Function: "main.run"},
		{File: "cmd/root.go", Line: 11, Function: "main.Resolve"},
	}}
	res := Extract(r, ctx, 3)
	block := res.Snippets[0].Lines[0]
	if !strings.Contains(block, "func Resolve") {
		t.Fatalf("symbol definition not surfaced:\n%s", block)
	}
}

func TestExtract_NilRepo(t *testing.T) {
	res := Extract(nil, &analyze.Context{Language: analyze.LangGo}, 5)
	if len(res.Snippets) != 0 {
		t.Fatalf("expected empty result for nil repo")
	}
}

func TestFormat_Empty(t *testing.T) {
	if Format(Result{}) != "" {
		t.Fatal("Format of empty result should be \"\"")
	}
}

func TestSymbolName(t *testing.T) {
	cases := map[string]string{
		"main.(*Server).Handle": "Handle",
		"main.run":              "run",
		"pkg.foo":               "foo",
		"Bar":                   "Bar",
		"foo()":                 "foo",
		"":                      "",
	}
	for in, want := range cases {
		if got := symbolName(in); got != want {
			t.Errorf("symbolName(%q) = %q, want %q", in, got, want)
		}
	}
}
