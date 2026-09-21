package models

import (
	"strings"
	"testing"
)

func TestBetaConfigMergeDefaultsFillsEmptyMessage(t *testing.T) {
	c := BetaConfig{Enabled: true, Message: "   "}
	c.MergeDefaults()
	if c.Message != DefaultBetaMessage {
		t.Fatalf("message vide non remplacé : %q", c.Message)
	}
}

// Le bandeau de connexion de l’app remplace par une erreur générique tout message de plus de
// 400 caractères : un texte tronqué reste lisible, un texte trop long ne s’affiche pas du tout.
func TestBetaConfigMergeDefaultsTruncates(t *testing.T) {
	c := BetaConfig{Message: strings.Repeat("é", 600)}
	c.MergeDefaults()
	if n := len([]rune(c.Message)); n != BetaMessageMaxLen {
		t.Fatalf("longueur après troncature = %d, attendu %d", n, BetaMessageMaxLen)
	}
	if BetaMessageMaxLen >= 400 {
		t.Fatalf("la limite doit rester sous le seuil d’affichage de l’app")
	}
}

func TestCanAccessDuringBeta(t *testing.T) {
	cases := []struct {
		name string
		user User
		want bool
	}{
		{"compte ordinaire", User{}, false},
		{"invité", User{BetaAccess: true}, true},
		// Sans cette exception, activer le verrou fermerait la console qui le désactive.
		{"administrateur non invité", User{Role: RoleAdmin}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.user.CanAccessDuringBeta(); got != tc.want {
				t.Fatalf("CanAccessDuringBeta() = %v, attendu %v", got, tc.want)
			}
		})
	}
}
