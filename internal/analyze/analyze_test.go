package analyze

import (
	"strings"
	"testing"
)

func TestDetectLanguage(t *testing.T) {
	cases := []struct {
		name string
		text string
		want Language
	}{
		{
			"go",
			"panic: runtime error: index out of range\n\ngoroutine 1 [running]:\nmain.run()\n\t/x/cmd/root.go:68 +0x3a",
			LangGo,
		},
		{
			"python",
			"Traceback (most recent call last):\n  File \"app.py\", line 42, in run\n    x[5]\nIndexError: list index out of range",
			LangPython,
		},
		{
			"java",
			"Exception in thread \"main\" java.lang.NullPointerException\n\tat com.foo.Bar.doThing(Bar.java:42)\nCaused by: java.io.IOException",
			LangJava,
		},
		{
			"node",
			"TypeError: Cannot read properties of null\n    at Object.<anonymous> (/x/app.js:10:15)\n    at Module._compile (node:internal/modules/cjs/loader:1101:14)",
			LangNode,
		},
		{
			"ruby",
			"/x/app.rb:42:in `block': undefined method (NoMethodError)",
			LangRuby,
		},
		{
			"rust",
			"thread 'main' panicked at 'index out of bounds', src/main.rs:42:7\nstack backtrace:\n   0: rust_begin_unwind",
			LangRust,
		},
		{
			"dotnet",
			"Unhandled exception. System.NullReferenceException: Object reference not set\n   at Program.Main() in /x/Program.cs:line 42",
			LangDotNet,
		},
		{
			"generic",
			"something went wrong undefined behavior",
			LangGeneric,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := DetectLanguage(c.text); got != c.want {
				t.Fatalf("DetectLanguage = %q, want %q", got, c.want)
			}
		})
	}
}

func TestParseStack_Go(t *testing.T) {
	text := "goroutine 1 [running]:\nmain.run()\n\t/Users/x/cmd/root.go:68 +0x3a\nmain.main()\n\t/Users/x/main.go:6 +0x12\n"
	frames := ParseStack(text, LangGo)
	if len(frames) != 2 {
		t.Fatalf("got %d frames, want 2", len(frames))
	}
	if frames[0].File != "/Users/x/cmd/root.go" || frames[0].Line != 68 || frames[0].Function != "main.run" {
		t.Fatalf("frame0 = %+v", frames[0])
	}
	if frames[1].File != "/Users/x/main.go" || frames[1].Line != 6 || frames[1].Function != "main.main" {
		t.Fatalf("frame1 = %+v", frames[1])
	}
}

func TestParseStack_Python(t *testing.T) {
	text := `Traceback (most recent call last):
  File "app.py", line 42, in run
    x[5]
IndexError: list index out of range`
	frames := ParseStack(text, LangPython)
	if len(frames) != 1 || frames[0].File != "app.py" || frames[0].Line != 42 || frames[0].Function != "run" {
		t.Fatalf("frames = %+v", frames)
	}
}

func TestParseStack_GenericFallback(t *testing.T) {
	// A bare "foo.go:5" with no Go-specific markers still yields frames.
	text := "error at utils.py:99 here and helpers.go:7 too"
	frames := ParseStack(text, LangGeneric)
	if len(frames) != 2 {
		t.Fatalf("got %d frames, want 2: %+v", len(frames), frames)
	}
}

func TestParseStack_Cap(t *testing.T) {
	var lines []string
	for i := 0; i < 100; i++ {
		lines = append(lines, strings.Repeat("x", i))
		lines = append(lines, "\t/x/file.go:"+itoa(i+1))
	}
	frames := ParseStack(strings.Join(lines, "\n"), LangGo)
	if len(frames) != MaxFrames {
		t.Fatalf("got %d frames, want cap %d", len(frames), MaxFrames)
	}
}

func TestGroupErrors_Dedup(t *testing.T) {
	text := strings.Repeat("2024-01-01T00:00:00Z ERROR connection refused\n", 5) +
		"2024-01-01T00:00:05Z ERROR disk full\n"
	groups := GroupErrors(text)
	if len(groups) != 2 {
		t.Fatalf("got %d groups, want 2: %+v", len(groups), groups)
	}
	if groups[0].Count != 5 {
		t.Fatalf("top group count = %d, want 5 (count desc sort)", groups[0].Count)
	}
	if groups[1].Count != 1 {
		t.Fatalf("second group count = %d, want 1", groups[1].Count)
	}
}

func TestGroupErrors_LineNumberCollapse(t *testing.T) {
	// Differing only by line number and hex should collapse to one group.
	text := "foo.go:1 bad 0xdead\n\nfoo.go:2 bad 0xbeef\n\nfoo.go:3 bad 0xcafe\n"
	groups := GroupErrors(text)
	if len(groups) != 1 {
		t.Fatalf("got %d groups, want 1 (line/hex normalized): %+v", len(groups), groups)
	}
	if groups[0].Count != 3 {
		t.Fatalf("count = %d, want 3", groups[0].Count)
	}
}

func TestContextJSON_Empty(t *testing.T) {
	c := &Context{Language: LangGeneric}
	if got := c.JSON(); got != "" {
		t.Fatalf("empty context JSON = %q, want \"\"", got)
	}
}

func TestContextJSON_NonEmpty(t *testing.T) {
	c := &Context{Language: LangGo, StackFrames: []Frame{{File: "a.go", Line: 1}}}
	if got := c.JSON(); !strings.Contains(got, `"language":"go"`) || !strings.Contains(got, `"a.go"`) {
		t.Fatalf("JSON missing fields: %q", got)
	}
}

func TestSignature_Grouped(t *testing.T) {
	// Two near-duplicate panics differing only in line numbers should share a
	// signature (line numbers are normalized away) and yield a representative.
	a := "panic: runtime error: index out of range [1] with length 0\nmain.go:12 +0x10"
	b := "panic: runtime error: index out of range [1] with length 0\nmain.go:99 +0x20"
	sigA, repA := Signature(a)
	sigB, repB := Signature(b)
	if sigA == "" || sigA != sigB {
		t.Fatalf("sigs should match and be non-empty: %q vs %q", sigA, sigB)
	}
	if repA == "" || repB == "" {
		t.Fatalf("representatives should be non-empty: %q %q", repA, repB)
	}
}

func TestSignature_Fallback(t *testing.T) {
	// A short message with no block markers and no dedup still produces a
	// stable signature via the normalize fallback.
	sig, rep := Signature("something went wrong here")
	if sig == "" || !strings.Contains(sig, "something went wrong here") {
		t.Fatalf("fallback sig = %q", sig)
	}
	if rep == "" {
		t.Fatalf("fallback rep = %q", rep)
	}
}

// itoa avoids strconv import in the test helper above.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
