package goalcalendar

import (
	"math"
	"time"

	"runapp/internal/strava"
)

const (
	// MinKmEpsilon tolère les arrondis Strava (m).
	MinKmEpsilon = 0.08
	// PaceToleranceSecPerKm : plancher de l'écart d'allure toléré quand on court
	// plus lentement que la cible. Au-delà, la tolérance suit l'allure (voir paceTolerance).
	PaceToleranceSecPerKm = 8.0
	// DoneShare : part de la distance prévue qui suffit pour « validée ». Un GPS
	// qui coupe un virage ou un retour à la maison 300 m plus tôt n'est pas un échec.
	DoneShare = 0.9
	// PartialShare : en dessous de cette part, la sortie ne remplit pas la séance.
	// Entre les deux, elle compte comme « partielle » : courue, mais écourtée.
	PartialShare = 0.5
)

// paceTolerance : écart toléré autour de la cible, plus large vers le rapide.
// Plus lent : 3 % (8 s minimum), soit ~11 s sur un footing à 6:00/km — l'erreur
// d'un GPS en ville. Plus rapide : 6 % (15 s minimum) ; un footing un peu vif
// reste un footing, c'est le coach qui en parle si ça devient une habitude.
func paceTolerance(target float64) (slower, faster float64) {
	return math.Max(PaceToleranceSecPerKm, 0.03*target), math.Max(15, 0.06*target)
}

// pacedOK : l'allure tient la cible, aux tolérances près.
func pacedOK(pace, target float64) bool {
	slower, faster := paceTolerance(target)
	return pace <= target+slower && pace >= target-faster
}

// longEnough : la sortie couvre au moins `share` de la distance prévue.
func longEnough(km, minKm, share float64) bool {
	return km+1e-6 >= share*minKm-MinKmEpsilon
}

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

// BestRunForSession choisit la sortie du jour : au moins la moitié de la distance
// prévue, la plus longue d'abord (une sortie validée vaut mieux qu'une partielle),
// puis la meilleure adéquation d'allure si une cible est définie.
func BestRunForSession(
	dayRuns []strava.RunActivity,
	minKm float64,
	paceTarget *float64,
	effortPace EffortPaceResolver,
) *strava.RunActivity {
	var candidates []strava.RunActivity
	for _, r := range dayRuns {
		if !longEnough(r.DistanceM/1000, minKm, PartialShare) {
			continue
		}
		candidates = append(candidates, r)
	}
	if len(candidates) == 0 {
		return nil
	}
	// Une sortie assez longue pour valider passe devant une sortie écourtée,
	// même mieux rythmée.
	var full []strava.RunActivity
	for _, r := range candidates {
		if longEnough(r.DistanceM/1000, minKm, DoneShare) {
			full = append(full, r)
		}
	}
	if len(full) > 0 {
		candidates = full
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
		if pacedOK(p, paceTarget) {
			return 0
		}
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
	if !longEnough(km, minKm, PartialShare) {
		return "missed"
	}
	if !longEnough(km, minKm, DoneShare) {
		return "partial"
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
		slower, _ := paceTolerance(*paceTarget)
		if ap <= 0 || ap <= *paceTarget+slower {
			return "done"
		}
		return "partial"
	}
	if pacedOK(ap, *paceTarget) {
		return "done"
	}
	return "partial"
}
