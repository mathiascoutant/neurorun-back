package handlers

import (
	"testing"
	"time"
)

func parisTime(t *testing.T, s string) time.Time {
	t.Helper()
	loc, err := time.LoadLocation("Europe/Paris")
	if err != nil {
		t.Skip("base tz absente sur l'hôte")
	}
	parsed, err := time.ParseInLocation("2006-01-02 15:04", s, loc)
	if err != nil {
		t.Fatalf("date de test invalide %q: %v", s, err)
	}
	return parsed
}

func TestWeekStart(t *testing.T) {
	cases := []struct {
		name string
		now  string
		want string
	}{
		{"lundi matin renvoie le jour même", "2026-09-07 08:30", "2026-09-07 00:00"},
		{"mercredi remonte au lundi", "2026-09-09 22:15", "2026-09-07 00:00"},
		// Le cas qui casse avec une semaine démarrant le dimanche, ou en UTC :
		// dimanche 23h à Paris appartient encore à la semaine du lundi précédent.
		{"dimanche soir reste sur la semaine en cours", "2026-09-13 23:30", "2026-09-07 00:00"},
		{"lundi minuit pile", "2026-09-07 00:00", "2026-09-07 00:00"},
		// Passage à l'heure d'hiver le dimanche 25 octobre 2026.
		{"semaine du changement d'heure", "2026-10-27 09:00", "2026-10-26 00:00"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := weekStart(parisTime(t, c.now))
			want := parisTime(t, c.want)
			if !got.Equal(want) {
				t.Errorf("weekStart(%s) = %s, attendu %s", c.now, got, want)
			}
			if got.Weekday() != time.Monday {
				t.Errorf("weekStart(%s) tombe un %s, attendu lundi", c.now, got.Weekday())
			}
		})
	}
}

// weekStart doit rester correct quel que soit le fuseau de l'instant fourni :
// l'heure serveur peut être en UTC alors que la semaine est parisienne.
func TestWeekStartIgnoreFuseauEntree(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Paris")
	if err != nil {
		t.Skip("base tz absente sur l'hôte")
	}
	// Dimanche 22:30 UTC : Paris est à +2 en septembre, on est donc déjà lundi 00:30
	// sur place, et la semaine doit basculer.
	utc := time.Date(2026, 9, 13, 22, 30, 0, 0, time.UTC)
	got := weekStart(utc)
	want := time.Date(2026, 9, 14, 0, 0, 0, 0, loc)
	if !got.Equal(want) {
		t.Errorf("weekStart(%s) = %s, attendu %s", utc, got, want)
	}
}
