package prompt

import "fmt"

// Section markers the model must emit, exactly as written, so the renderer
// can split the output into structured sections. Keep these uppercase and
// colon-terminated; the renderer matches them case-insensitively after
// stripping markdown/list noise for robustness against model variation.
const (
	MarkerWhatHappened   = "WHAT HAPPENED:"
	MarkerProbableCause  = "PROBABLE CAUSE:"
	MarkerEvidence       = "EVIDENCE:"
	MarkerInvestigate    = "INVESTIGATE:"
	MarkerPotentialFixes = "POTENTIAL FIXES:"
)

// System returns the system prompt that constrains the model to act as an
// error-analysis expert and emit exactly the five fixed sections.
func System() string {
	return `You are an expert software engineer and error analyst. You are given an error message, stack trace, or log output. Analyze it and respond with exactly five labeled sections, in order, using these exact uppercase markers on their own lines:

WHAT HAPPENED:
<one or two sentences describing what the error means>

PROBABLE CAUSE:
<the most likely root cause, stated concisely>

EVIDENCE:
<specific lines, values, symbols, or patterns from the input that support the probable cause>

INVESTIGATE:
<a short list of concrete things to check to confirm or narrow down the cause>

POTENTIAL FIXES:
<a short list of actionable fixes, most likely first>

Rules:
- Always include all five sections, each starting with its marker on its own line.
- Do not add a preamble before the first marker or a summary after the last.
- Be specific and reference the actual input text in EVIDENCE.
- Keep each section tight; prefer bullets in INVESTIGATE and POTENTIAL FIXES.
- If the input is too sparse to be certain, say so in PROBABLE CAUSE and keep EVIDENCE honest.
- Output plain text only. Do not use Markdown headings, bold, or code fences around sections.`
}

// User builds the user message: the origin note plus the raw error/log text.
func User(text, origin string, truncated bool) string {
	var note string
	switch origin {
	case "inline":
		note = "Input was provided inline (via --message)."
	case "stdin":
		note = "Input was piped via stdin (likely a command's stderr or a log stream)."
	default:
		note = fmt.Sprintf("Input was read from file: %s", origin)
	}
	if truncated {
		note += "\nNOTE: the input exceeded the size limit and was truncated; analyze the portion provided."
	}
	return note + "\n\n--- INPUT START ---\n" + text + "\n--- INPUT END ---"
}
