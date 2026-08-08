package render

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Section is one parsed part of the model output: a heading plus its body.
type Section struct {
	Title string
	Body  string
	Style lipgloss.Style
}

// labels is the canonical set of section headings, in order. matchMarker
// compares a line against these after stripping markdown noise and case.
var labels = []string{
	"WHAT HAPPENED",
	"PROBABLE CAUSE",
	"EVIDENCE",
	"INVESTIGATE",
	"POTENTIAL FIXES",
}

var (
	styleWhatHappened   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("51"))  // bright cyan
	styleProbableCause  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("214")) // orange/yellow
	styleEvidence       = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39"))  // blue
	styleInvestigate    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("207")) // magenta
	stylePotentialFixes = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("46"))  // green
	bodyStyle           = lipgloss.NewStyle().MarginLeft(2).MarginBottom(1)
)

var labelToStyle = map[string]lipgloss.Style{
	"WHAT HAPPENED":   styleWhatHappened,
	"PROBABLE CAUSE":  styleProbableCause,
	"EVIDENCE":        styleEvidence,
	"INVESTIGATE":     styleInvestigate,
	"POTENTIAL FIXES": stylePotentialFixes,
}

var labelOrder = map[string]int{
	"WHAT HAPPENED":   0,
	"PROBABLE CAUSE":  1,
	"EVIDENCE":        2,
	"INVESTIGATE":     3,
	"POTENTIAL FIXES": 4,
}

// Parse splits content on the five section markers. Matching is robust to
// common model quirks: leading markdown (#, -, *) and surrounding
// whitespace/punctuation are stripped before comparing, case-insensitively.
// Sections found out of order are still emitted; Parse returns nil if no
// markers are present at all (degenerate output).
func Parse(content string) []Section {
	lines := strings.Split(content, "\n")
	var sections []Section
	var current *Section

	for _, line := range lines {
		if label, ok := matchMarker(line); ok {
			sections = append(sections, Section{
				Title: label,
				Style: labelToStyle[label],
			})
			current = &sections[len(sections)-1]
			continue
		}
		if current != nil {
			current.Body += line + "\n"
		}
	}

	for i := range sections {
		sections[i].Body = strings.TrimSpace(sections[i].Body)
	}

	if len(sections) == 0 {
		return nil
	}
	return sections
}

// matchMarker returns the canonical label if line is (or starts with) one
// of the section markers, after normalization.
func matchMarker(line string) (string, bool) {
	// Strip leading markdown/list decoration and whitespace.
	trimmed := strings.TrimLeft(line, " \t#*-")
	trimmed = strings.TrimSpace(trimmed)
	trimmed = strings.TrimRight(trimmed, ":")
	upper := strings.ToUpper(trimmed)

	for _, m := range labels {
		// Match exact label, or a label followed by extra text on the same line
		// (some models append the first sentence to the marker line).
		if upper == m || strings.HasPrefix(upper, m+" ") {
			return m, true
		}
	}
	return "", false
}

// Render returns the colorized, structured view of the model output. If the
// output contains no recognizable markers, the raw text is shown under a
// single neutral heading so the tool never fails to surface something.
func Render(content string) string {
	sections := Parse(content)
	if len(sections) == 0 {
		fallback := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("244"))
		heading := fallback.Render("MODEL RESPONSE (no sections parsed)")
		return heading + "\n" + strings.TrimSpace(content)
	}

	var b strings.Builder
	for _, s := range sections {
		b.WriteString(s.Style.Render(s.Title))
		b.WriteString("\n")
		b.WriteString(bodyStyle.Render(strings.TrimSpace(s.Body)))
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}
