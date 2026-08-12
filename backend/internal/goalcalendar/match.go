package goalcalendar

import (
	"math"
	"time"

	"runapp/internal/strava"
)

const (
	// MinKmEpsilon tolère les arrondis Strava (m).
	MinKmEpsilon = 0.08
	// PaceToleranceSecPerKm écart allure moyenne pour « validé » vs « partiel ».
	PaceToleranceSecPerKm = 5.0
)

// PaceSecPerKm allure moyenne sur la sortie (s/km).
func PaceSecPerKm(r strava.RunActivity) float64 {
	km := r.DistanceM / 1000
	if km < 0.01 {
		return 0
	}
	return float64(r.MovingSec) / km
}

// IsInterval dit si la sortie est une séance à intervalles (fractionné).
func IsInterval(r strava.RunActivity) bool {
	return strava.IsIntervalWorkout(r.WorkoutType, r.Name)
}

// EffortPaceResolver renvoie l'allure des seuls efforts d'un fractionné, sans les
// récupérations (nil si le découpage n'a pas pu être établi). Le handler l'injecte :
// lui seul sait interroger Strava et mettre le résultat en cache.
type EffortPaceResolver func(r strava.RunActivity) *float64

// JudgedPaceSecPerKm est l'allure à comparer à la cible du plan : sur un fractionné
// celle des efforts, ailleurs la moyenne de la sortie. Renvoie 0 pour un fractionné
// dont on ne sait pas isoler les efforts — son allure moyenne, récupérations
// comprises, décrit une séance qui n'a pas été courue.
func JudgedPaceSecPerKm(r strava.RunActivity, effortPace EffortPaceResolver) (pace float64, interval bool) {
	if !IsInterval(r) {
		return PaceSecPerKm(r), false
	}
	if effortPace != nil {
		if p := effortPace(r); p != nil && *p > 0 {
			return *p, true
		}
	}
	return 0, true
}

func runsOnLocalDate(runs []strava.RunActivity, day time.Time, loc *time.Location) []strava.RunActivity {
	if loc == nil {
		loc = time.UTC
	}
	y, m, d := day.In(loc).Date()
	var out []strava.RunActivity
	for _, r := range runs {
		ry, rm, rd := r.StartAt.In(loc).Date()
		if ry == y && rm == m && rd == d {
			out = append(out, r)
		}
	}
	return out
}

// BestRunForSession choisit la sortie du jour : distance >= minKm, meilleure adéquation d’allure si cible définie.
func BestRunForSession(
	dayRuns []strava.RunActivity,
	minKm float64,
	paceTarget *float64,
	effortPace EffortPaceResolver,
) *strava.RunActivity {
	var candidates []strava.RunActivity
	for _, r := range dayRuns {
		km := r.DistanceM / 1000
		if km+1e-6 < minKm-MinKmEpsilon {
			continue
		}
		candidates = append(candidates, r)
	}
	if len(candidates) == 0 {
		return nil
	}
	if paceTarget == nil || *paceTarget <= 0 {
		best := &candidates[0]
		bestKm := best.DistanceM / 1000
		for i := 1; i < len(candidates); i++ {
			km := candidates[i].DistanceM / 1000
			if km > bestKm {
				best = &candidates[i]
				bestKm = km
			}
		}
		return best
	}
	best := &candidates[0]
	bestDiff := paceMismatch(*best, *paceTarget, effortPace)
	for i := 1; i < len(candidates); i++ {
		d := paceMismatch(candidates[i], *paceTarget, effortPace)
		if d < bestDiff || (d == bestDiff && candidates[i].DistanceM > best.DistanceM) {
			best = &candidates[i]
			bestDiff = d
		}
	}
	return best
}

// paceMismatch mesure l'écart à la cible : 0 = la sortie colle. Sur un fractionné,
// seule compte l'allure des efforts, et courir plus vite que la cible n'est pas un
// écart ; sans découpage exploitable, l'allure ne départage pas.
func paceMismatch(r strava.RunActivity, paceTarget float64, effortPace EffortPaceResolver) float64 {
	p, interval := JudgedPaceSecPerKm(r, effortPace)
	if !interval {
		return math.Abs(p - paceTarget)
	}
	if p <= 0 {
		return 0
	}
	return math.Max(0, p-paceTarget)
}

// SessionStatus pour une séance et une date locale.
func SessionStatus(
	now time.Time,
	sessionDay time.Time,
	loc *time.Location,
	runs []strava.RunActivity,
	minKm float64,
	paceTarget *float64,
	effortPace EffortPaceResolver,
) (status string, matched *strava.RunActivity) {
	if loc == nil {
		loc = time.UTC
	}
	sd := sessionDay.In(loc)
	today := now.In(loc)
	sDate := time.Date(sd.Year(), sd.Month(), sd.Day(), 0, 0, 0, 0, loc)
	tDate := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, loc)

	dayRuns := runsOnLocalDate(runs, sDate, loc)
	best := BestRunForSession(dayRuns, minKm, paceTarget, effortPace)

	if sDate.After(tDate) {
		return "upcoming", nil
	}
	if sDate.Equal(tDate) {
		if best == nil {
			return "upcoming", nil
		}
		return finalizeStatus(best, minKm, paceTarget, effortPace), best
	}
	if best == nil {
		return "missed", nil
	}
	return finalizeStatus(best, minKm, paceTarget, effortPace), best
}

func finalizeStatus(
	r *strava.RunActivity,
	minKm float64,
	paceTarget *float64,
	effortPace EffortPaceResolver,
) string {
	km := r.DistanceM / 1000
	if km+1e-6 < minKm-MinKmEpsilon {
		return "missed"
	}
	if paceTarget == nil || *paceTarget <= 0 {
		return "done"
	}
	ap, interval := JudgedPaceSecPerKm(*r, effortPace)
	if interval {
		// Fractionné : les récupérations n'ont pas à peser dans le verdict. Seul
		// compte d'avoir tenu l'allure sur les efforts, et aller plus vite que la
		// cible est une réussite, pas un écart. Sans découpage exploitable, la
		// distance suffit : juger la moyenne reviendrait à sanctionner la récup.
		if ap <= 0 || ap <= *paceTarget+PaceToleranceSecPerKm {
			return "done"
		}
		return "partial"
	}
	if math.Abs(ap-*paceTarget) <= PaceToleranceSecPerKm {
		return "done"
	}
	return "partial"
}
