package models

import (
	"encoding/json"
	"strings"
	"testing"

	"runapp/internal/vma"
)

// Un fractionné n'a pas de kilomètres notés : `kms` doit sortir en liste vide,
// sans rien perdre des autres champs de la note, sinon l'app plante à
// l'ouverture de la course.
func TestRunScoreKmsJamaisNull(t *testing.T) {
	s := &RunScore{
		Result:      vma.Result{Scorable: true, Total: 84, SessionType: vma.SessionInterval, Reps: []vma.RepScore{{Rep: 1}}},
		VmaTestID:   "abc",
		Explanation: &RunScoreExplanation{Summary: "résumé"},
	}
	for _, v := range []any{s, *s, LiveRun{Score: s}} {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{`"kms":[]`, `"total":84`, `"reps":[`, `"vma_test_id":"abc"`, `"summary":"résumé"`} {
			if !strings.Contains(string(b), want) {
				t.Fatalf("%s absent de %s", want, b)
			}
		}
	}
}
