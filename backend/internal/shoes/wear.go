package shoes

import (
	"math"
	"time"
)

// États d'usure, du plus neuf au plus usé.
const (
	StatusNew     = "new"     // moins de la moitié de la durée de vie
	StatusGood    = "good"    // jusqu'aux trois quarts
	StatusWatch   = "watch"   // jusqu'à 90 % : l'amorti commence à baisser
	StatusReplace = "replace" // dernière ligne droite
	StatusWorn    = "worn"    // durée de vie théorique dépassée
)

// Wear : usure théorique d'une paire.
type Wear struct {
	UsedKm      float64 `json:"used_km"`
	LifespanKm  float64 `json:"lifespan_km"`
	RemainingKm float64 `json:"remaining_km"`
	// WearPct : part de la durée de vie consommée, en pour cent. Dépasse 100
	// quand la paire a fait plus que prévu.
	WearPct float64 `json:"wear_pct"`
	Status  string  `json:"status"`
	// KmPerWeek : rythme moyen de la paire depuis qu'on l'a. Zéro tant qu'elle
	// n'a pas roulé.
	KmPerWeek float64 `json:"km_per_week"`
	// WeeksLeft / ReplaceOn : projection à ce rythme. Absents sans rythme, ou
	// une fois la paire usée.
	WeeksLeft *float64 `json:"weeks_left,omitempty"`
	ReplaceOn string   `json:"replace_on,omitempty"`
}

// ComputeWear : usure d'une paire qui a fait usedKm depuis startedOn.
//
// Le rythme se mesure sur toute la vie de la paire, avec au moins une semaine
// au dénominateur : une paire achetée hier avec 10 km ne projette pas
// « 70 km par semaine ».
func ComputeWear(usedKm, lifespanKm float64, startedOn, now time.Time) Wear {
	if lifespanKm <= 0 {
		lifespanKm = LifespanFor(CategoryDaily)
	}
	usedKm = math.Max(usedKm, 0)
	w := Wear{
		UsedKm:      round1(usedKm),
		LifespanKm:  lifespanKm,
		RemainingKm: round1(math.Max(lifespanKm-usedKm, 0)),
		WearPct:     round1(usedKm / lifespanKm * 100),
	}
	switch {
	case w.WearPct < 50:
		w.Status = StatusNew
	case w.WearPct < 75:
		w.Status = StatusGood
	case w.WearPct < 90:
		w.Status = StatusWatch
	case w.WearPct < 100:
		w.Status = StatusReplace
	default:
		w.Status = StatusWorn
	}

	weeks := math.Max(now.Sub(startedOn).Hours()/(24*7), 1)
	if usedKm <= 0 {
		return w
	}
	w.KmPerWeek = round1(usedKm / weeks)
	if w.Status == StatusWorn || w.KmPerWeek <= 0 {
		return w
	}
	left := round1(w.RemainingKm / (usedKm / weeks))
	w.WeeksLeft = &left
	w.ReplaceOn = now.Add(time.Duration(left * 7 * 24 * float64(time.Hour))).Format("2006-01-02")
	return w
}

func round1(x float64) float64 {
	return math.Round(x*10) / 10
}
