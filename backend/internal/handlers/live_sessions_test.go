package handlers

import (
	"strings"
	"testing"

	"runapp/internal/models"
)

func TestLiveStartNotificationText(t *testing.T) {
	cases := []struct {
		sessionType string
		targetKm    float64
		wantTitle   string
	}{
		{"", 5, "🏃 Mathias vient de commencer son 5 km"},
		{models.RunTypeFree, 0, "🏃 Mathias vient de commencer une sortie libre"},
		{models.RunTypeEndurance, 0, "🏃 Mathias vient de commencer une sortie endurance"},
		{models.RunTypeInterval, 0, "⚡ Mathias vient de commencer un fractionné"},
		{models.RunTypeInterval, 8, "⚡ Mathias vient de commencer son fractionné de 8 km"},
		{models.RunTypeEndurance, 12.4, "🏃 Mathias vient de commencer son endurance de 12 km"},
		{models.RunTypeFree, 7.5, "🏃 Mathias vient de commencer son 7,5 km"},
	}
	for _, c := range cases {
		title, body := liveStartNotificationText("Mathias", c.sessionType, c.targetKm)
		if title != c.wantTitle {
			t.Errorf("(%q, %v) titre = %q, attendu %q", c.sessionType, c.targetKm, title, c.wantTitle)
		}
		if body == "" {
			t.Errorf("(%q, %v) corps vide", c.sessionType, c.targetKm)
		}
	}
}

func TestLiveBoostMessageSpokenHasNoEmoji(t *testing.T) {
	for _, kind := range []string{models.BoostKindBoost, models.BoostKindFire, models.BoostKindRespect} {
		text, spoken := liveBoostMessage("Léa", kind)
		if !strings.HasPrefix(text, spoken) {
			t.Errorf("%s : le texte %q devrait reprendre la phrase parlée %q", kind, text, spoken)
		}
		for _, r := range spoken {
			if r > 0x2000 && r != '’' {
				t.Errorf("%s : la phrase parlée %q contient %q", kind, spoken, r)
			}
		}
	}
}

func TestValidLiveSessionType(t *testing.T) {
	for _, ok := range []string{"", "free", "endurance", "interval"} {
		if !validLiveSessionType(ok) {
			t.Errorf("%q devrait être accepté", ok)
		}
	}
	if validLiveSessionType("tempo") {
		t.Error("un type inconnu doit être refusé")
	}
}
