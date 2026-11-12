// Package leaderboard : score global et classements Monde / Europe / pays.
//
// Chaque course enregistrée par NeuroRun (téléphone, Apple Watch, Garmin) rapporte
// des points, figés à l'enregistrement : un changement de barème ne fait pas sauter
// le classement. Les courses importées de Strava ou Suunto ne comptent pas, faute de
// pouvoir les contrôler.
package leaderboard

import (
	"math"
	"time"
)

// Garde-fous : en dehors, la course ne rapporte rien. Ils écartent les erreurs de
// capteur et les trajets en vélo ou en voiture, sans plafonner le volume.
const (
	MinDistanceM        = 1000
	MinPaceSecPerKm     = 150 // 2:30/km
	MaxPaceSecPerKm     = 900 // 15:00/km
	MaxImpliedSpeedKmh  = 30
	maxNoteMultiplierUp = 2 // note 100 → ×(1 + 2) = ×3
)

// Run : ce que le calcul lit d'une course.
type Run struct {
	DistanceM          float64
	MovingSec          float64
	AvgPaceSecPerKm    float64
	MaxImpliedSpeedKmh float64
	// Note : note sur 100, nil quand la course n'est pas notée (pas de test VMA,
	// course non notable). Elle compte alors ×1.
	Note *int
}

// Outcome : les points d'une course et, s'ils valent zéro, pourquoi.
type Outcome struct {
	Points  float64
	Counted bool
	// Reason : vide si la course compte ; sinon « too_short », « pace », « speed ».
	Reason string
}

// Multiplier : de ×1 (note 0 ou absente) à ×3 (note 100). La courbe au carré
// récompense surtout le haut de l'échelle, où le barème veut qu'au-dessus de 90
// reste dur.
func Multiplier(note *int) float64 {
	if note == nil {
		return 1
	}
	n := math.Max(0, math.Min(100, float64(*note))) / 100
	return 1 + maxNoteMultiplierUp*n*n
}

// Points : km × multiplicateur, arrondi au dixième. Chaque kilomètre vaut au moins
// un point : courir beaucoup paie, bien courir paie davantage.
func Points(r Run) Outcome {
	if r.DistanceM < MinDistanceM {
		return Outcome{Reason: "too_short"}
	}
	pace := r.AvgPaceSecPerKm
	if pace <= 0 && r.MovingSec > 0 {
		pace = r.MovingSec / (r.DistanceM / 1000)
	}
	if pace < MinPaceSecPerKm || pace > MaxPaceSecPerKm {
		return Outcome{Reason: "pace"}
	}
	if r.MaxImpliedSpeedKmh > MaxImpliedSpeedKmh {
		return Outcome{Reason: "speed"}
	}
	km := r.DistanceM / 1000
	return Outcome{Points: math.Round(km*Multiplier(r.Note)*10) / 10, Counted: true}
}

func paris() *time.Location {
	if loc, err := time.LoadLocation("Europe/Paris"); err == nil {
		return loc
	}
	return time.UTC
}

// SeasonStart : une saison est un mois civil, à l'heure de Paris.
func SeasonStart(now time.Time) time.Time {
	t := now.In(paris())
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, t.Location())
}

// SeasonEnd : début de la saison suivante.
func SeasonEnd(now time.Time) time.Time {
	return SeasonStart(now).AddDate(0, 1, 0)
}
