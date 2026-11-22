package agent

import (
	"strings"
	"testing"
)

func TestVerdictFloor(t *testing.T) {
	cases := []struct {
		name  string
		blast string
		want  string
	}{
		{"critical", `{"score":40,"verdict":"high","findings":[{"severity":"critical"}]}`, "UNSOUND"},
		{"severe score", `{"score":100,"verdict":"severe","findings":[]}`, "UNSOUND"},
		{"elevated score", `{"score":60,"verdict":"elevated","findings":[]}`, "CONCERNS"},
		{"low score", `{"score":10,"verdict":"low","findings":[]}`, ""},
		{"bad json", `not json`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := verdictFloor(tc.blast)
			if got != tc.want {
				t.Fatalf("verdictFloor = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestEnforceFloor(t *testing.T) {
	in := "## Verdict: CONCERNS\nSome summary.\n\n## Evidence\n..."
	got := enforceFloor(in, "UNSOUND", "blast score 100 (severe)")
	if !strings.HasPrefix(got, "## Verdict: UNSOUND\n") {
		t.Fatalf("verdict not escalated: %q", got)
	}
	if !strings.Contains(got, "Deterministic floor applied") {
		t.Fatal("missing escalation note")
	}
	if !strings.Contains(got, "Some summary.") {
		t.Fatal("summary was dropped")
	}

	unchanged := enforceFloor(in, "CONCERNS", "blast score 60 (elevated)")
	if unchanged != in {
		t.Fatalf("verdict should be unchanged, got %q", unchanged)
	}
}
