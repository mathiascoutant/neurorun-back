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
func BestRunForSession(dayRuns []strava.RunActivity, minKm float64, paceTarget *float64) *strava.RunActivity {
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
	bestDiff := math.Abs(PaceSecPerKm(*best) - *paceTarget)
	for i := 1; i < len(candidates); i++ {
		d := math.Abs(PaceSecPerKm(candidates[i]) - *paceTarget)
		if d < bestDiff || (d == bestDiff && candidates[i].DistanceM > best.DistanceM) {
			best = &candidates[i]
			bestDiff = d
		}
	}
	return best
}

// SessionStatus pour une séance et une date locale.
func SessionStatus(
	now time.Time,
	sessionDay time.Time,
	loc *time.Location,
	runs []strava.RunActivity,
	minKm float64,
	paceTarget *float64,
) (status string, matched *strava.RunActivity) {
	if loc == nil {
		loc = time.UTC
	}
	sd := sessionDay.In(loc)
	today := now.In(loc)
	sDate := time.Date(sd.Year(), sd.Month(), sd.Day(), 0, 0, 0, 0, loc)
	tDate := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, loc)

	dayRuns := runsOnLocalDate(runs, sDate, loc)
	best := BestRunForSession(dayRuns, minKm, paceTarget)

	if sDate.After(tDate) {
		return "upcoming", nil
	}
	if sDate.Equal(tDate) {
		if best == nil {
			return "upcoming", nil
		}
		return finalizeStatus(best, minKm, paceTarget), best
	}
	if best == nil {
		return "missed", nil
	}
	return finalizeStatus(best, minKm, paceTarget), best
}

func finalizeStatus(r *strava.RunActivity, minKm float64, paceTarget *float64) string {
	km := r.DistanceM / 1000
	if km+1e-6 < minKm-MinKmEpsilon {
		return "missed"
	}
	if paceTarget == nil || *paceTarget <= 0 {
		return "done"
	}
	ap := PaceSecPerKm(*r)
	if math.Abs(ap-*paceTarget) <= PaceToleranceSecPerKm {
		return "done"
	}
	return "partial"
}
