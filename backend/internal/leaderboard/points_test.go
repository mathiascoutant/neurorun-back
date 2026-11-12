package leaderboard

import (
	"testing"
	"time"
)

func note(n int) *int { return &n }

func run(km float64, paceSec float64, n *int) Run {
	return Run{DistanceM: km * 1000, MovingSec: km * paceSec, AvgPaceSecPerKm: paceSec, MaxImpliedSpeedKmh: 18, Note: n}
}

// Les exemples validés avec l'utilisateur : une longue sortie bien notée doit
// battre une sortie plus longue mais moyenne.
func TestExemplesValides(t *testing.T) {
	cases := []struct {
		name string
		r    Run
		want float64
	}{
		{"10 km noté 60", run(10, 330, note(60)), 17.2},
		{"19 km noté 50", run(19, 330, note(50)), 28.5},
		{"19 km noté 90", run(19, 330, note(90)), 49.8},
		{"30 km noté 50", run(30, 330, note(50)), 45},
		{"30 km noté 90", run(30, 330, note(90)), 78.6},
		{"10 km sans note", run(10, 330, nil), 10},
		{"10 km noté 100", run(10, 330, note(100)), 30},
	}
	for _, c := range cases {
		got := Points(c.r)
		if !got.Counted || got.Points != c.want {
			t.Errorf("%s : %v points (compté %v), attendu %v", c.name, got.Points, got.Counted, c.want)
		}
	}
	if Points(run(19, 330, note(90))).Points <= Points(run(30, 330, note(50))).Points {
		t.Error("19 km noté 90 doit battre 30 km noté 50")
	}
}

func TestGardeFous(t *testing.T) {
	cases := []struct {
		name   string
		r      Run
		reason string
	}{
		{"moins de 1 km", run(0.9, 330, note(80)), "too_short"},
		{"trop rapide (vélo)", run(10, 120, note(80)), "pace"},
		{"trop lent (marche)", run(5, 960, note(80)), "pace"},
		{"pointe de vitesse (voiture)", Run{DistanceM: 10000, AvgPaceSecPerKm: 330, MaxImpliedSpeedKmh: 45}, "speed"},
	}
	for _, c := range cases {
		got := Points(c.r)
		if got.Counted || got.Points != 0 || got.Reason != c.reason {
			t.Errorf("%s : %+v, attendu 0 point (%s)", c.name, got, c.reason)
		}
	}
	// Les bornes elles-mêmes comptent.
	if !Points(run(1, 150, nil)).Counted || !Points(run(1, 900, nil)).Counted {
		t.Error("2:30/km et 15:00/km doivent compter")
	}
}

// Sans allure moyenne transmise, on la déduit du temps de déplacement.
func TestAllureDeduite(t *testing.T) {
	r := Run{DistanceM: 5000, MovingSec: 5 * 100} // 1:40/km → vélo
	if Points(r).Counted {
		t.Error("allure déduite de 1:40/km : la course ne doit pas compter")
	}
}

func TestSaison(t *testing.T) {
	// 31 octobre 23 h 30 UTC = 1er novembre 0 h 30 à Paris.
	now := time.Date(2026, 10, 31, 23, 30, 0, 0, time.UTC)
	start := SeasonStart(now)
	if start.Month() != time.November || start.Day() != 1 {
		t.Errorf("saison : %v, attendu 1er novembre", start)
	}
	if end := SeasonEnd(now); end.Month() != time.December {
		t.Errorf("fin de saison : %v, attendu 1er décembre", end)
	}
}

func TestPays(t *testing.T) {
	if c, ok := NormalizeCountry(" fr "); !ok || c != "FR" {
		t.Errorf("fr → %q %v", c, ok)
	}
	if _, ok := NormalizeCountry("ZZ"); ok {
		t.Error("ZZ ne doit pas être accepté")
	}
	if !IsEurope("BE") || IsEurope("CA") || IsEurope("RE") {
		t.Error("Europe : BE oui, CA et RE non")
	}
	for _, c := range EuropeCountries() {
		if _, ok := NormalizeCountry(c); !ok {
			t.Errorf("%s est en Europe mais pas dans la liste ISO", c)
		}
	}
}
