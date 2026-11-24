package agent

import (
	"encoding/json"
	"fmt"
	"strings"
)

// blastScan is the subset of `blast scan --json` we need to set a verdict floor.
type blastScan struct {
	Score    int    `json:"score"`
	Verdict  string `json:"verdict"`
	Findings []struct {
		Severity string `json:"severity"`
	} `json:"findings"`
}

// verdictFloor maps deterministic Blast results to a minimum verdict. The
// model may be more severe than the floor, but never less severe.
func verdictFloor(blast string) (string, string) {
	var b blastScan
	if err := json.Unmarshal([]byte(blast), &b); err != nil {
		return "", ""
	}
	critical, high := 0, 0
	for _, f := range b.Findings {
		switch {
		case strings.EqualFold(f.Severity, "critical"):
			critical++
		case strings.EqualFold(f.Severity, "high"):
			high++
		}
	}
	if critical > 0 {
		return "UNSOUND", fmt.Sprintf("blast found %d critical finding(s)", critical)
	}
	if b.Score >= 75 || strings.EqualFold(b.Verdict, "severe") {
		return "UNSOUND", fmt.Sprintf("blast score %d (severe)", b.Score)
	}
	if b.Score >= 50 || strings.EqualFold(b.Verdict, "elevated") || high > 0 {
		return "CONCERNS", fmt.Sprintf("blast score %d, %d high finding(s)", b.Score, high)
	}
	return "", ""
}

var verdictRank = map[string]int{"SOUND": 0, "CONCERNS": 1, "UNSOUND": 2}

// enforceFloor escalates a model verdict when it falls below the deterministic
// floor, and annotates the output so the escalation is transparent.
func enforceFloor(text, floor, reason string) string {
	if floor == "" || reason == "" {
		return text
	}
	line := "## Verdict: "
	idx := strings.Index(text, line)
	if idx < 0 {
		return text
	}
	rest := text[idx+len(line):]
	end := strings.IndexAny(rest, "\r\n")
	if end < 0 {
		end = len(rest)
	}
	current := strings.ToUpper(strings.TrimSpace(rest[:end]))
	if verdictRank[current] >= verdictRank[floor] {
		return text
	}
	note := fmt.Sprintf("\n> Deterministic floor applied: %s requires at least **%s**.\n", reason, floor)
	return text[:idx] + line + floor + note + rest[end:]
}
