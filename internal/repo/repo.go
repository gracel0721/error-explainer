// Package repo provides minimal git integration for source extraction: it
// locates a repository root, resolves stack-trace file paths against it, reads
// file lines, and greps for symbol definitions. It shells out to git via
// os/exec — git is a soft dependency for the --repo feature and degrades
// gracefully when absent.
package repo

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// MaxFileBytes caps a single file read so huge generated files don't blow up.
const MaxFileBytes = 2 * 1024 * 1024

// Repo is a resolved git repository rooted at Root.
type Repo struct {
	Root string
}

// GrepHit is one matched line from git grep.
type GrepHit struct {
	File string
	Line int
	Text string
}

// Find locates the git repo containing start (a directory or file path). It
// returns an error if git is missing or start is not inside a repo; callers
// treat this as "skip source extraction".
func Find(start string) (*Repo, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return nil, errors.New("git not found in PATH")
	}
	abs, err := filepath.Abs(start)
	if err != nil {
		return nil, err
	}
	if info, err := os.Stat(abs); err == nil && !info.IsDir() {
		abs = filepath.Dir(abs)
	}
	out, err := exec.Command("git", "-C", abs, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return nil, errors.New("not a git repository: " + abs)
	}
	root := strings.TrimSpace(string(out))
	if root == "" {
		return nil, errors.New("empty repo root")
	}
	return &Repo{Root: root}, nil
}

// containerPrefixes are common build/container roots that should be stripped
// when mapping a trace path onto the repo.
var containerPrefixes = []string{
	"/workspace/", "/app/", "/build/", "/src/", "/repo/", "/root/",
}

// genericBasenames are too common to resolve by basename alone.
var genericBasenames = map[string]bool{
	"main.go": true, "index.js": true, "index.ts": true, "Makefile": true,
	"go.mod": true, "setup.py": true, "package.json": true, "app.py": true,
}

// Resolve maps a stack-trace file path (absolute, container-prefixed, or
// repo-relative) to an existing regular file under Root. Returns the path
// and true on success, or "" and false when nothing matches.
func (r *Repo) Resolve(path string) (string, bool) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", false
	}

	// 1. Absolute path as-is.
	if filepath.IsAbs(path) && fileExists(path) {
		return path, true
	}

	// 2. Strip known container prefixes, then try under Root.
	for _, pfx := range containerPrefixes {
		if strings.HasPrefix(path, pfx) {
			stripped := strings.TrimPrefix(path, pfx)
			if cand := filepath.Join(r.Root, stripped); fileExists(cand) {
				return cand, true
			}
		}
	}

	// 3. Repo-relative join directly.
	if cand := filepath.Join(r.Root, path); fileExists(cand) {
		return cand, true
	}

	// 4. Strip leading path components until the remainder exists under Root
	//    (handles Go module-path traces like github.com/x/y/internal/foo).
	remainder := path
	for {
		next := strings.IndexByte(remainder, '/')
		if next < 0 {
			break
		}
		remainder = remainder[next+1:]
		if cand := filepath.Join(r.Root, remainder); fileExists(cand) {
			return cand, true
		}
	}

	// 5. Unique-basename match across the working tree (gated, no generic names).
	base := filepath.Base(path)
	if !genericBasenames[base] {
		if cand, ok := r.uniqueBasename(base); ok {
			return cand, true
		}
	}

	return "", false
}

func (r *Repo) uniqueBasename(base string) (string, bool) {
	var match string
	count := 0
	filepath.WalkDir(r.Root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if d.Name() == base {
			match = p
			count++
		}
		return nil
	})
	if count == 1 && match != "" {
		return match, true
	}
	return "", false
}

// ReadFileLines reads the file (relative to Root or absolute) and returns its
// lines. Files larger than MaxFileBytes return an error.
func (r *Repo) ReadFileLines(path string) ([]string, error) {
	abs := path
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(r.Root, path)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, err
	}
	if info.Size() > MaxFileBytes {
		return nil, errors.New("file exceeds size cap")
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, err
	}
	return strings.Split(string(data), "\n"), nil
}

// Grep runs `git grep -n -E pattern -- pathspec` from Root and returns hits.
// Empty pathspec means "search everything".
func (r *Repo) Grep(pattern, pathspec string) ([]GrepHit, error) {
	// Order matters: git grep takes the pattern before "--" and pathspecs
	// after. Putting the pattern last would make git treat it as a pathspec.
	args := []string{"-C", r.Root, "grep", "-n", "-E", "--full-name", pattern}
	if pathspec != "" {
		// A plain "*.ext" pathspec matches recursively in git grep (the
		// default wildmatch crosses "/"), so no glob magic is needed.
		args = append(args, "--", pathspec)
	}
	cmd := exec.Command("git", args...)
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		// git grep exits non-zero on no matches — not a real error.
		if out.Len() == 0 {
			return nil, nil
		}
		return nil, err
	}
	return parseGrep(out.String())
}

var grepLine = regexp.MustCompile(`^(.+?):(\d+):(.*)$`)

func parseGrep(s string) ([]GrepHit, error) {
	var hits []GrepHit
	for _, line := range strings.Split(s, "\n") {
		if line == "" {
			continue
		}
		m := grepLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		n, _ := strconv.Atoi(m[2])
		hits = append(hits, GrepHit{File: m[1], Line: n, Text: m[3]})
	}
	return hits, nil
}

// fileExists reports whether path is a regular file.
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
