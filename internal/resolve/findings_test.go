package resolve

import (
	"reflect"
	"testing"

	"github.com/minihci/tink/internal/finding"
)

// A plan's findings are one list. Blocked is the errors, Warnings the warnings, Notes the notes, and Drift the warnings that carry the reason of image
// drift, which Warnings leaves out; each keeps the order the findings were raised in.
func TestPlannedResource_SelectsFindingsBySeverityAndReason(t *testing.T) {
	p := PlannedResource{Findings: []finding.Finding{
		finding.Notef("n", "", "note 1"),
		finding.Warnf("w", "", "warning 1"),
		finding.Errorf("e", "", "error 1"),
		finding.Warnf(ReasonImageDrift, "", "drift 1"),
		finding.Warnf("w", "", "warning 2"),
		finding.Errorf("e", "", "error 2"),
		finding.Warnf(ReasonImageDrift, "", "drift 2"),
		finding.Notef("n", "", "note 2"),
	}}
	for name, c := range map[string]struct {
		got  []finding.Finding
		want []string
	}{
		"Blocked":  {p.Blocked(), []string{"error 1", "error 2"}},
		"Warnings": {p.Warnings(), []string{"warning 1", "warning 2"}},
		"Notes":    {p.Notes(), []string{"note 1", "note 2"}},
		"Drift":    {p.Drift(), []string{"drift 1", "drift 2"}},
	} {
		if got := details(c.got); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s = %q, want %q", name, got, c.want)
		}
	}
	if len(PlannedResource{}.Blocked()) != 0 || len(PlannedResource{}.Drift()) != 0 {
		t.Error("a plan with no findings has none of any kind")
	}
}
