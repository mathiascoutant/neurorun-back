package shoes

import (
	"testing"
	"time"
)

func TestProfilDeCourse(t *testing.T) {
	cases := []struct {
		runType, scored string
		km, gain        float64
		want            string
	}{
		{"free", "endurance", 10, 80, ProfileEasy},
		{"free", "", 12, 320, ProfileHilly},
		// 6 km et 180 m : 30 m/km, c'est déjà un trail.
		{"endurance", "endurance", 6, 180, ProfileHilly},
		// Court et pentu : sous 3 km, le ratio ne compte pas.
		{"free", "endurance", 2, 80, ProfileEasy},
		{"interval", "", 8, 40, ProfileFast},
		{"free", "tempo", 10, 50, ProfileFast},
		{"free", "race", 5, 20, ProfileFast},
	}
	for _, c := range cases {
		if got := RunProfile(c.runType, c.scored, c.km, c.gain); got != c.want {
			t.Errorf("RunProfile(%q, %q, %.0f km, %.0f m) = %q, attendu %q", c.runType, c.scored, c.km, c.gain, got, c.want)
		}
	}
}

var (
	pegasus  = Candidate{ID: "peg", Name: "Pegasus 41", Category: CategoryDaily, WearPct: 40}
	endorph  = Candidate{ID: "endo", Name: "Endorphin Pro 4", Category: CategoryRace, WearPct: 20}
	speedgoa = Candidate{ID: "sg", Name: "Speedgoat 6", Category: CategoryTrail, WearPct: 10}
)

func TestDevinetteParCategorieSansHistorique(t *testing.T) {
	cands := []Candidate{pegasus, endorph, speedgoa}
	for profile, want := range map[string]string{
		ProfileFast:  "endo",
		ProfileHilly: "sg",
		ProfileEasy:  "peg",
	} {
		s := Guess(profile, time.Tuesday, cands, nil)
		if s == nil || s.ShoeID != want {
			t.Errorf("%s : %+v, attendu %s", profile, s, want)
		}
	}
	// Deux paires d'entraînement et rien pour les départager : on ne coche rien.
	other := Candidate{ID: "nimbus", Name: "Nimbus 27", Category: CategoryCushion, WearPct: 30}
	if s := Guess(ProfileEasy, time.Tuesday, []Candidate{pegasus, other}, nil); s != nil {
		t.Errorf("deux paires d'entraînement sans historique : %+v, attendu rien", s)
	}
}

func TestLHabitudePasseAvantLaCategorie(t *testing.T) {
	cands := []Candidate{pegasus, endorph}
	// Ses fractionnés, il les court en Pegasus.
	history := []PastRun{
		{ShoeID: "peg", Profile: ProfileFast, Weekday: time.Thursday},
		{ShoeID: "peg", Profile: ProfileFast, Weekday: time.Tuesday},
		{ShoeID: "endo", Profile: ProfileFast, Weekday: time.Saturday},
	}
	s := Guess(ProfileFast, time.Tuesday, cands, history)
	if s == nil || s.ShoeID != "peg" || s.Reason != profileHabit[ProfileFast] {
		t.Fatalf("habitude ignorée : %+v", s)
	}
}

func TestHabitudeDuJour(t *testing.T) {
	other := Candidate{ID: "nimbus", Name: "Nimbus 27", Category: CategoryCushion, WearPct: 30}
	cands := []Candidate{pegasus, other}
	history := []PastRun{
		{ShoeID: "nimbus", Profile: ProfileEasy, Weekday: time.Tuesday},
		{ShoeID: "nimbus", Profile: ProfileEasy, Weekday: time.Tuesday},
		{ShoeID: "peg", Profile: ProfileEasy, Weekday: time.Sunday},
		{ShoeID: "peg", Profile: ProfileEasy, Weekday: time.Sunday},
		{ShoeID: "peg", Profile: ProfileEasy, Weekday: time.Sunday},
	}
	s := Guess(ProfileEasy, time.Tuesday, cands, history)
	if s == nil || s.ShoeID != "nimbus" || s.Reason != "Celle que tu portes d’habitude le mardi" {
		t.Fatalf("mardi : %+v", s)
	}
	// Le jeudi, rien de propre au jour : l'habitude générale l'emporte.
	s = Guess(ProfileEasy, time.Thursday, cands, history)
	if s == nil || s.ShoeID != "peg" {
		t.Fatalf("jeudi : %+v", s)
	}
}

func TestEgaliteSansHabitude(t *testing.T) {
	if id := habit([]PastRun{{ShoeID: "a"}, {ShoeID: "b"}, {ShoeID: "a"}, {ShoeID: "b"}}); id != "" {
		t.Fatalf("égalité tranchée au hasard : %q", id)
	}
	if id := habit([]PastRun{{ShoeID: "a"}}); id != "" {
		t.Fatalf("une seule course n'est pas une habitude : %q", id)
	}
}

func TestConseilSelonLUsure(t *testing.T) {
	worn := pegasus
	worn.WearPct = 82
	// Séance rapide, paire habituelle à 82 % : on sort la plus fraîche.
	history := []PastRun{
		{ShoeID: "peg", Profile: ProfileFast}, {ShoeID: "peg", Profile: ProfileFast},
	}
	s := Advise(ProfileFast, time.Tuesday, []Candidate{worn, endorph}, history)
	if s == nil || s.ShoeID != "endo" {
		t.Fatalf("séance rapide sur paire fatiguée : %+v", s)
	}
	if want := "Ta Pegasus 41 est à 82 % : garde-la pour l’endurance et sors ta Endorphin Pro 4 pour le fractionné."; s.Tip != want {
		t.Fatalf("conseil : %q", s.Tip)
	}

	// Paire de compétition fatiguée : on la garde pour le jour J.
	tired := endorph
	tired.WearPct = 85
	s = Advise(ProfileFast, time.Tuesday, []Candidate{pegasus, tired}, nil)
	if want := "Ta Endorphin Pro 4 est à 85 % : garde-la pour le jour de course et sors ta Pegasus 41 pour le fractionné."; s == nil || s.ShoeID != "peg" || s.Tip != want {
		t.Fatalf("paire de compétition usée : %+v", s)
	}

	// En endurance, on garde la paire mais on invite à alterner dès 90 %.
	worn.WearPct = 93
	fresh := Candidate{ID: "nimbus", Name: "Nimbus 27", Category: CategoryCushion, WearPct: 15}
	easy := []PastRun{{ShoeID: "peg", Profile: ProfileEasy}, {ShoeID: "peg", Profile: ProfileEasy}}
	s = Advise(ProfileEasy, time.Tuesday, []Candidate{worn, fresh}, easy)
	if s == nil || s.ShoeID != "peg" || s.Tip == "" {
		t.Fatalf("endurance sur paire à 93 %% : %+v", s)
	}

	// Une paire de compétition ne remplace pas une paire d'entraînement usée.
	s = Advise(ProfileEasy, time.Tuesday, []Candidate{worn, endorph}, easy)
	if s == nil || s.ShoeID != "peg" || s.Tip != "" {
		t.Fatalf("endurance, seule alternative en compétition : %+v", s)
	}

	// Au-delà de 100 %, on change de paire.
	worn.WearPct = 104
	s = Advise(ProfileEasy, time.Tuesday, []Candidate{worn, fresh}, easy)
	if s == nil || s.ShoeID != "nimbus" {
		t.Fatalf("paire usée : %+v", s)
	}
}
