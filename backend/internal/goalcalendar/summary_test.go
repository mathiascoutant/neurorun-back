package goalcalendar

import "testing"

func TestSummaryForDistance(t *testing.T) {
	cases := []struct {
		in   string
		km   float64
		want string
	}{
		{"Footing facile de 5 km", 7, "Footing facile"},
		{"Footing facile de 5 km", 5.2, "Footing facile de 5 km"},
		{"Sortie longue 18 km en endurance", 21, "Sortie longue en endurance"},
		{"Footing (5 km) en Z2", 7, "Footing en Z2"},
		{"Footing d'environ 6,5 km", 8, "Footing"},
		{"Fractionné 3x2 km", 9, "Fractionné 3x2 km"},
		{"Fractionné 3 × 2 km au seuil", 9, "Fractionné 3 × 2 km au seuil"},
		{"Fractionné 6x400 m", 8, "Fractionné 6x400 m"},
		{"Seuil 2 km + 3 km", 9, "Seuil 2 km + 3 km"},
		{"5 km", 7, "5 km"},
		{"Footing 40 min", 7, "Footing 40 min"},
	}
	for _, c := range cases {
		if got := SummaryForDistance(c.in, c.km); got != c.want {
			t.Errorf("SummaryForDistance(%q, %v) = %q, attendu %q", c.in, c.km, got, c.want)
		}
	}
}
