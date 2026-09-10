package vma

import (
	"math"
	"sort"
)

// Réglages de la notation. Ils fixent à eux seuls la difficulté de l'échelle :
// une note au-dessus de 95 doit rester rare, une bonne sortie tourne entre
// 75 et 90.
const (
	// Fourchette d'affichage : ±2 s autour de l'objectif, soit 4:58–5:02 pour
	// un objectif à 5:00. Elle sert à compter les kilomètres « dans la cible »,
	// pas à offrir des points : la courbe décroît dès la première seconde.
	kmToleranceSec = 2.0

	// Courbe de notation, en secondes d'écart par kilomètre :
	//   points = 100 · exp( -(écart / kmFalloffSec) ^ kmFalloffPower )
	// L'exposant sous 2 rend la pente franche dès les premières secondes, ce
	// qui réserve le haut de l'échelle : 3 s/km coûtent déjà 4 points, alors
	// qu'une gaussienne classique les laissait à 100.
	// Repères : 0 s → 100, 3 s → 96, 6 s → 89, 10 s → 78, 25 s → 37.
	kmFalloffSec   = 25.0
	kmFalloffPower = 1.5

	// La régularité pèse plus lourd que le chrono : un temps final juste
	// obtenu en alternant les à-coups vaut moins qu'une course tenue.
	weightRegularity = 0.70
	weightChrono     = 0.30

	// Distance minimale pour qu'une note ait du sens.
	MinScorableKm = 1.0

	// Au-delà de cet écart en avance sur l'objectif, la VMA de référence est
	// probablement dépassée : on invite à refaire le test.
	staleVmaPct = 3.0
)

// Split : un kilomètre complet tel que remonté par l'app ou la montre.
type Split struct {
	Km           int
	PaceSecPerKm float64
}

// KmScore : détail d'un kilomètre dans la note.
type KmScore struct {
	Km           int     `json:"km"`
	PaceSecPerKm float64 `json:"pace_sec_per_km"`
	// DeltaSec : écart signé à l'allure visée (négatif = plus rapide).
	DeltaSec float64 `json:"delta_sec"`
	Points   float64 `json:"points"`
}

// Result : note complète d'une course.
type Result struct {
	Scorable bool   `json:"scorable"`
	Reason   string `json:"reason,omitempty"` // pourquoi non notable

	Total      int    `json:"total"`
	Regularity int    `json:"regularity"`
	Chrono     int    `json:"chrono"`
	Band       string `json:"band"`

	VmaKmh             float64 `json:"vma_kmh"`
	DistanceKm         float64 `json:"distance_km"`
	TargetPaceSecPerKm float64 `json:"target_pace_sec_per_km"`
	TargetTimeSec      float64 `json:"target_time_sec"`
	ActualTimeSec      float64 `json:"actual_time_sec"`
	// ChronoDeltaSec : écart signé au temps visé (négatif = plus rapide).
	ChronoDeltaSec float64 `json:"chrono_delta_sec"`

	Kms []KmScore `json:"kms"`

	// Indicateurs de lecture, réutilisés pour l'explication.
	KmsInBand     int     `json:"kms_in_band"`
	BestKm        int     `json:"best_km"`
	WorstKm       int     `json:"worst_km"`
	SpreadSec     float64 `json:"spread_sec"`       // écart max-min entre kms
	MeanAbsDevSec float64 `json:"mean_abs_dev_sec"` // écart moyen à l'objectif
	DriftSec      float64 `json:"drift_sec"`        // 2e moitié - 1re moitié
	StaleVmaHint  bool    `json:"stale_vma_hint"`
}

// deviationPoints note un écart à l'allure visée, exprimé en secondes par
// kilomètre. Même courbe pour la régularité et pour le chrono : les deux
// parlent la même unité, donc l'échelle se comporte à l'identique que l'on
// coure à 3:30 ou à 6:30 au kilomètre.
func deviationPoints(devSecPerKm float64) float64 {
	d := math.Abs(devSecPerKm)
	if d == 0 {
		return 100
	}
	return 100 * math.Exp(-math.Pow(d/kmFalloffSec, kmFalloffPower))
}

// bandFor nomme la tranche de note.
func bandFor(total int) string {
	switch {
	case total >= 95:
		return "exceptionnel"
	case total >= 90:
		return "tres_bon"
	case total >= 75:
		return "bon"
	case total >= 60:
		return "correct"
	default:
		return "a_travailler"
	}
}

// Score note une course sur 100 en la comparant à l'allure que la VMA rend
// tenable sur la distance réellement parcourue.
//
// Deux composantes : l'adhérence kilomètre par kilomètre à l'allure visée
// (70 %) et l'écart au chrono final (30 %). Un temps final parfait obtenu
// avec des kilomètres en dents de scie ne peut donc pas décrocher une note
// haute.
func Score(splits []Split, distanceKm, movingSec, vmaKmh float64) Result {
	res := Result{VmaKmh: vmaKmh, DistanceKm: distanceKm, ActualTimeSec: movingSec}

	if vmaKmh <= 0 {
		res.Reason = "vma_absente"
		return res
	}
	if distanceKm < MinScorableKm {
		res.Reason = "distance_trop_courte"
		return res
	}
	if movingSec <= 0 {
		res.Reason = "duree_invalide"
		return res
	}

	target := TargetFor(vmaKmh, distanceKm)
	res.TargetPaceSecPerKm = target.PaceSecPerKm
	res.TargetTimeSec = target.TimeSec

	// On ne note que les kilomètres complets : le dernier tronçon partiel a une
	// allure trop bruitée pour être comparée.
	complete := make([]Split, 0, len(splits))
	for _, s := range splits {
		if s.PaceSecPerKm > 0 && float64(s.Km) <= math.Floor(distanceKm)+0.001 {
			complete = append(complete, s)
		}
	}
	sort.Slice(complete, func(i, j int) bool { return complete[i].Km < complete[j].Km })
	if len(complete) == 0 {
		res.Reason = "aucun_km_complet"
		return res
	}

	var sumPoints, sumAbsDev float64
	bestPoints, worstPoints := -1.0, 101.0
	minPace, maxPace := math.Inf(1), math.Inf(-1)

	res.Kms = make([]KmScore, 0, len(complete))
	for _, s := range complete {
		delta := s.PaceSecPerKm - target.PaceSecPerKm
		pts := deviationPoints(delta)
		res.Kms = append(res.Kms, KmScore{
			Km:           s.Km,
			PaceSecPerKm: s.PaceSecPerKm,
			DeltaSec:     math.Round(delta),
			Points:       math.Round(pts*10) / 10,
		})
		sumPoints += pts
		sumAbsDev += math.Abs(delta)
		if math.Abs(delta) <= kmToleranceSec {
			res.KmsInBand++
		}
		if pts > bestPoints {
			bestPoints, res.BestKm = pts, s.Km
		}
		if pts < worstPoints {
			worstPoints, res.WorstKm = pts, s.Km
		}
		minPace = math.Min(minPace, s.PaceSecPerKm)
		maxPace = math.Max(maxPace, s.PaceSecPerKm)
	}

	n := float64(len(complete))
	regularity := sumPoints / n
	res.MeanAbsDevSec = math.Round(sumAbsDev/n*10) / 10
	res.SpreadSec = math.Round(maxPace - minPace)

	// Dérive : moyenne de la seconde moitié moins celle de la première.
	// Positif = la course s'est délitée sur la fin.
	if len(complete) >= 2 {
		half := len(complete) / 2
		var firstSum, lastSum float64
		for _, s := range complete[:half] {
			firstSum += s.PaceSecPerKm
		}
		for _, s := range complete[len(complete)-half:] {
			lastSum += s.PaceSecPerKm
		}
		res.DriftSec = math.Round((lastSum - firstSum) / float64(half))
	}

	chronoDelta := movingSec - target.TimeSec
	res.ChronoDeltaSec = math.Round(chronoDelta)
	// Ramené en secondes par kilomètre : « j'ai perdu 4 s/km sur l'ensemble »
	// se compare directement aux écarts kilomètre par kilomètre.
	chrono := deviationPoints(chronoDelta / distanceKm)

	total := weightRegularity*regularity + weightChrono*chrono

	res.Scorable = true
	res.Regularity = int(math.Round(regularity))
	res.Chrono = int(math.Round(chrono))
	res.Total = int(math.Round(total))
	res.Band = bandFor(res.Total)
	// Nettement plus rapide que prévu : la VMA de référence a vieilli. Ce
	// repère-là reste relatif — 3 % d'avance ne pèsent pas pareil sur 5 km et
	// sur un marathon.
	res.StaleVmaHint = chronoDelta/target.TimeSec*100 < -staleVmaPct

	return res
}
