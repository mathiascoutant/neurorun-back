package weather

import "math"

const (
	// Froid : rien jusqu'à coldFreeC. En dessous, les muscles et le souffle
	// coûtent un peu plus, 0,25 % par degré : -5 °C ≈ 2,5 %. C'est bien moins
	// que la chaleur, d'où le plafond bas.
	coldFreeC     = 5.0
	coldPctPerDeg = 0.25
	maxColdPct    = 4.0

	// Vent : la source le mesure à 10 m, le coureur le reçoit vers 1,5 m, où il
	// souffle environ 30 % moins fort. Sur une boucle, le vent de face coûte plus
	// que le vent de dos ne rend (la résistance de l'air croît avec le carré de
	// la vitesse relative) : il reste un coût net, ~0,4 % × (vent au sol en
	// km/h / 10)². Repères : 20 km/h → 0 (moins de 1 %), 30 km/h ≈ 1,8 %,
	// 40 km/h ≈ 3,1 %.
	windGroundShare  = 0.7
	windPctPerTen2   = 0.4
	maxWindPct       = 6.0
	maxSlowdownPct   = 15.0
	minSlowdownShown = 1.0
)

// ColdSlowdownPct : ralentissement par temps froid, en %. Zéro en deçà de 1 %.
func ColdSlowdownPct(tempC float64) float64 {
	if tempC >= coldFreeC {
		return 0
	}
	pct := math.Min(maxColdPct, (coldFreeC-tempC)*coldPctPerDeg)
	if pct < minSlowdownShown {
		return 0
	}
	return round1(pct)
}

// WindSlowdownPct : coût net du vent sur une boucle, en %. Zéro en deçà de 1 %.
func WindSlowdownPct(windKmh float64) float64 {
	if windKmh <= 0 {
		return 0
	}
	ground := windKmh * windGroundShare / 10
	pct := math.Min(maxWindPct, windPctPerTen2*ground*ground)
	if pct < minSlowdownShown {
		return 0
	}
	return round1(pct)
}

func (c Conditions) total() float64 {
	return round1(math.Min(maxSlowdownPct, c.HeatPct+c.ColdPct+c.WindPct))
}

// RaceSlowdownPct : ralentissement sur une épreuve d'environ durationSec.
//
// La chaleur et le froid pèsent avec la durée : sur un 5 km, le corps n'a pas
// le temps de surchauffer comme sur un marathon. On n'en compte que la moitié
// sous 20 minutes, la totalité au-delà de 3 heures. Le vent, lui, coûte à
// chaque foulée : il compte en entier quelle que soit la distance.
func (c Conditions) RaceSlowdownPct(durationSec float64) float64 {
	share := 1.0
	if durationSec > 0 {
		share = math.Max(0.5, math.Min(1, 0.5+(durationSec/60-20)/320))
	}
	pct := (c.HeatPct+c.ColdPct)*share + c.WindPct
	return round1(math.Min(maxSlowdownPct, pct))
}
