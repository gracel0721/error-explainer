package repo

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// setupRepo creates a temp git repo with a couple of nested files:
//
//	<root>/cmd/root.go
//	<root>/internal/foo/bar.go
func setupRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "cmd", "root.go"), "package cmd\n")
	mustWrite(t, filepath.Join(root, "internal", "foo", "bar.go"), "package foo\n")
	mustGit(t, root, "init")
	mustGit(t, root, "add", "-A")
	mustGit(t, root, "commit", "-m", "init")
	return root
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := execGit(dir, args...)
	if err := cmd.Run(); err != nil {
		t.Fatalf("git %v in %s: %v", args, dir, err)
	}
}

func execGit(dir string, args ...string) *exec.Cmd {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	// Tests don't need a configured identity; silence git's commit-time checks.
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
	)
	return cmd
}

func TestFind(t *testing.T) {
	root := setupRepo(t)
	root, _ = filepath.EvalSymlinks(root) // git resolves the /var -> /private/var symlink
	r, err := Find(filepath.Join(root, "cmd"))
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if r.Root != root {
		t.Fatalf("Root = %q, want %q", r.Root, root)
	}
}

func TestFind_NotRepo(t *testing.T) {
	tmp := t.TempDir()
	if _, err := Find(tmp); err == nil {
		t.Fatal("expected error for non-repo dir")
	}
}

func TestResolve(t *testing.T) {
	root := setupRepo(t)
	r := &Repo{Root: root}

	cases := []struct {
		name string
		path string
		want string
	}{
		{"repo-relative", "cmd/root.go", filepath.Join(root, "cmd", "root.go")},
		{"absolute", filepath.Join(root, "cmd", "root.go"), filepath.Join(root, "cmd", "root.go")},
		{"container prefix", "/workspace/cmd/root.go", filepath.Join(root, "cmd", "root.go")},
		{"module path prefix", "github.com/x/y/cmd/root.go", filepath.Join(root, "cmd", "root.go")},
		{"unique basename", "some/random/dir/bar.go", filepath.Join(root, "internal", "foo", "bar.go")},
		{"missing file", "nope.go", ""},
		{"generic basename rejected", "x/y/z/main.go", ""}, // main.go is in repo root; basename match skipped as generic
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := r.Resolve(c.path)
			if c.want == "" {
				if ok {
					t.Fatalf("Resolve(%q) = %q, want miss", c.path, got)
				}
				return
			}
			if !ok || got != c.want {
				t.Fatalf("Resolve(%q) = (%q, %v), want (%q, true)", c.path, got, ok, c.want)
			}
		})
	}
}

func TestReadFileLines(t *testing.T) {
	root := setupRepo(t)
	r := &Repo{Root: root}
	lines, err := r.ReadFileLines("cmd/root.go")
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) == 0 || lines[0] != "package cmd" {
		t.Fatalf("lines = %v", lines)
	}
}

func TestGrep(t *testing.T) {
	root := setupRepo(t)
	r := &Repo{Root: root}
	hits, err := r.Grep(`package foo`, "*.go")
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].File != filepath.Join("internal", "foo", "bar.go") {
		t.Fatalf("hits = %+v", hits)
	}
}
