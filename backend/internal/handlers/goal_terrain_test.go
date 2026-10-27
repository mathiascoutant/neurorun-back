package handlers

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"runapp/internal/models"
)

// Le terrain arrive du client : il doit se normaliser (absent → route) et refuser
// toute autre valeur, sans quoi une faute de frappe partirait en base puis dans
// les consignes envoyées au coach.
func TestValidateGoalPayloadTerrain(t *testing.T) {
	cas := []struct {
		nom     string
		envoye  string
		attendu string
		refuse  bool
	}{
		{nom: "absent", envoye: `"target_time":"50 min"`, attendu: models.GoalTerrainRoad},
		{nom: "vide", envoye: `"target_time":"50 min","terrain":""`, attendu: models.GoalTerrainRoad},
		{nom: "route", envoye: `"target_time":"50 min","terrain":"road"`, attendu: models.GoalTerrainRoad},
		{nom: "trail", envoye: `"target_time":"50 min","terrain":"trail"`, attendu: models.GoalTerrainTrail},
		{nom: "casse libre", envoye: `"target_time":"50 min","terrain":" Trail "`, attendu: models.GoalTerrainTrail},
		{nom: "inconnu", envoye: `"target_time":"50 min","terrain":"vtt"`, refuse: true},
	}

	for _, c := range cas {
		t.Run(c.nom, func(t *testing.T) {
			body := `{"distance_km":10,"weeks":8,"sessions_per_week":3,` + c.envoye + `}`
			r := httptest.NewRequest("POST", "/api/goals", strings.NewReader(body))

			b, _, _, _, errHTTP, errMsg := validateGoalPayload(r)
			if c.refuse {
				if errHTTP == 0 {
					t.Fatalf("terrain %q accepté alors qu'il devait être refusé", c.envoye)
				}
				if !strings.Contains(errMsg, "terrain") {
					t.Fatalf("message peu clair pour le client : %q", errMsg)
				}
				return
			}
			if errHTTP != 0 {
				t.Fatalf("refus inattendu (%d) : %s", errHTTP, errMsg)
			}
			if b.Terrain != c.attendu {
				t.Fatalf("terrain = %q, attendu %q", b.Terrain, c.attendu)
			}
		})
	}
}

// EffectiveTerrain protège la lecture : les objectifs créés avant le champ n'ont
// pas de terrain en base et décrivent tous une course sur route.
func TestEffectiveTerrain(t *testing.T) {
	for brut, attendu := range map[string]string{
		"":      models.GoalTerrainRoad,
		"road":  models.GoalTerrainRoad,
		"trail": models.GoalTerrainTrail,
		"vtt":   models.GoalTerrainRoad,
	} {
		g := &models.Goal{Terrain: brut}
		if got := g.EffectiveTerrain(); got != attendu {
			t.Fatalf("EffectiveTerrain(%q) = %q, attendu %q", brut, got, attendu)
		}
	}
}

// La date de course est facultative, mais quand elle est là elle doit être lisible et
// tenir dans la même fenêtre que les semaines : c'est elle qui servira de compte à rebours.
func TestValidateGoalPayloadRaceDate(t *testing.T) {
	now := time.Now().UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	day := func(offset int) string { return today.AddDate(0, 0, offset).Format("2006-01-02") }

	cas := []struct {
		nom     string
		envoye  string
		attendu string
		refuse  string
	}{
		{nom: "absente", envoye: "", attendu: ""},
		{nom: "vide", envoye: " ", attendu: ""},
		{nom: "dans huit semaines", envoye: day(56), attendu: day(56)},
		{nom: "aujourd'hui", envoye: day(0), attendu: day(0)},
		{nom: "hier", envoye: day(-1), refuse: "passée"},
		{nom: "au-delà de 52 semaines", envoye: day(52*7 + 1), refuse: "52 semaines"},
		{nom: "illisible", envoye: "12 décembre", refuse: "AAAA-MM-JJ"},
	}

	for _, c := range cas {
		t.Run(c.nom, func(t *testing.T) {
			body := fmt.Sprintf(
				`{"distance_km":10,"weeks":8,"sessions_per_week":3,"target_time":"50 min","race_date":%q}`,
				c.envoye,
			)
			r := httptest.NewRequest("POST", "/api/goals", strings.NewReader(body))

			b, _, _, _, errHTTP, errMsg := validateGoalPayload(r)
			if c.refuse != "" {
				if errHTTP == 0 {
					t.Fatalf("date %q acceptée alors qu'elle devait être refusée", c.envoye)
				}
				if !strings.Contains(errMsg, c.refuse) {
					t.Fatalf("message %q ne dit pas %q", errMsg, c.refuse)
				}
				return
			}
			if errHTTP != 0 {
				t.Fatalf("refus inattendu (%d) : %s", errHTTP, errMsg)
			}
			if b.RaceDate != c.attendu {
				t.Fatalf("race_date = %q, attendu %q", b.RaceDate, c.attendu)
			}
		})
	}
}
