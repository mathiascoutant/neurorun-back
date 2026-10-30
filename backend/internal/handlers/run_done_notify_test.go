package handlers

import (
	"strings"
	"testing"

	"runapp/internal/models"
	"runapp/internal/vma"
)

func TestRunDoneNotificationText(t *testing.T) {
	score := func(n int) *models.RunScore { return &models.RunScore{Result: vma.Result{Total: n}} }
	cases := []struct {
		name        string
		firstName   string
		run         models.LiveRun
		title, body []string
	}{
		{
			"10 km notés 71",
			"Mathias",
			models.LiveRun{DistanceM: 10200, MovingSec: 3690, AvgPaceSecPerKm: 362, Score: score(71)},
			[]string{"💪 Bravo Mathias, 10,2 km bouclés !"},
			[]string{"10,2 km en 1h01'30 à 6:02/km.", "Ta note : 71/100, touche pour voir"},
		},
		{
			"séance du plan, très bien notée",
			"Mathias",
			models.LiveRun{DistanceM: 6000, MovingSec: 1800, AvgPaceSecPerKm: 300, Score: score(92), GoalSession: &models.LiveRunGoalSession{Week: 2, Session: 1}},
			[]string{"Bien joué Mathias, 6,0 km"},
			[]string{"Séance de ton plan validée ✅ 6,0 km en 30 min", "92/100, une très bonne course !"},
		},
		{
			"sans prénom ni note",
			"  ",
			models.LiveRun{DistanceM: 3000, MovingSec: 1080, AvgPaceSecPerKm: 360},
			[]string{"👏 Bravo, chaque sortie compte !"},
			[]string{"3,0 km en 18 min à 6:00/km."},
		},
		{
			"marathon",
			"Léa",
			models.LiveRun{DistanceM: 42195, MovingSec: 14400, AvgPaceSecPerKm: 341, Score: score(96)},
			[]string{"🏅 42,2 km bouclés, chapeau Léa !"},
			[]string{"exceptionnelle"},
		},
	}
	for _, c := range cases {
		title, body := runDoneNotificationText(c.firstName, &c.run)
		for _, want := range c.title {
			if !strings.Contains(title, want) {
				t.Errorf("%s : titre %q, attendu %q", c.name, title, want)
			}
		}
		for _, want := range c.body {
			if !strings.Contains(body, want) {
				t.Errorf("%s : corps %q, attendu %q", c.name, body, want)
			}
		}
		if strings.Contains(title, "Un ami") || strings.Contains(title, "  ") {
			t.Errorf("%s : titre mal formé %q", c.name, title)
		}
	}
}
