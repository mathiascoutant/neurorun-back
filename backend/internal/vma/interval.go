package vma

import (
	"math"
	"sort"
)

// Fractionné : la séance se juge sur ses répétitions. L'allure moyenne et la
// régularité au kilomètre mélangent efforts et récupérations voulues, elles
// ne disent rien de la façon dont la séance a été menée.
const (
	// Il faut au moins trois répétitions pour parler de régularité ou de tenue.
	minIntervalReps = 3

	// Régularité : écart de chaque répétition à l'allure médiane. Plus raide
	// que pour une sortie continue : une répétition se court à l'allure
	// choisie, il n'y a ni côte ni fatigue de fond pour l'excuser.
	// 3 s/km → 90, 6 s/km → 73, 10 s/km → 51.
	repFalloffSec = 13.0

	// Tenue : écart d'allure entre le dernier et le premier tiers des
	// répétitions. 3 s/km → 91, 6 s/km → 78, 10 s/km → 58.
	fadeFalloffSec = 15.0

	// Un effort de fractionné se court au moins à l'allure du seuil : plus
	// lent, la répétition n'a pas fait travailler ce qu'elle visait. La pente
	// est raide : 5 s/km sous le seuil → 76, 10 s/km → 47, 20 s/km → 12.
	intervalEffortFloor = 0.85
	effortFalloffSec    = 12.0

	// Récupérations : écart moyen de durée à la récupération médiane, en %.
	// 5 % → 93, 10 % → 83, 20 % → 58. Le découpage par la vitesse place les
	// bornes à quelques secondes près : la pente reste douce.
	recoveryFalloffPct = 30.0
)

// Poids d'un fractionné. La régularité et la tenue des répétitions font la
// séance ; l'intensité ne coûte que si les efforts sont trop lents, mais
// alors lourdement : des « efforts » en endurance ne font pas un fractionné.
var intervalWeights = map[string]float64{"reps": 40, "fade": 25, "effort": 20, "recoveries": 15}

// IntervalSegment : une répétition ou une récupération.
type IntervalSegment struct {
	DistanceM float64
	Sec       float64
	HrBpm     float64
}

func (s IntervalSegment) pace() float64 {
	if s.DistanceM <= 0 {
		return 0
	}
	return s.Sec / (s.DistanceM / 1000)
}

// IntervalInput : découpage effort / récupération, dans l'ordre de la séance.
// RecoveryAfter[i] est la récupération qui suit la répétition i (0 = aucune,
// la dernière n'en a souvent pas).
type IntervalInput struct {
	Reps          []IntervalSegment
	RecoveryAfter []float64
}

func (in *IntervalInput) usable() bool {
	if in == nil || len(in.Reps) < minIntervalReps {
		return false
	}
	for _, r := range in.Reps {
		if r.pace() <= 0 {
			return false
		}
	}
	return true
}

// RepScore : une répétition dans la note.
type RepScore struct {
	Rep          int     `json:"rep"`
	DistanceM    float64 `json:"distance_m"`
	Sec          float64 `json:"sec"`
	PaceSecPerKm float64 `json:"pace_sec_per_km"`
	// DeltaSec : écart à l'allure médiane des répétitions (négatif = plus vite).
	DeltaSec float64 `json:"delta_sec"`
	Points   float64 `json:"points"`
	// RecoverySec : récupération qui suit (0 = aucune).
	RecoverySec float64 `json:"recovery_sec,omitempty"`
	HrBpm       float64 `json:"hr_bpm,omitempty"`
}

func median(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

// scoreIntervals note un fractionné sur ses répétitions.
func scoreIntervals(res *Result, in *IntervalInput, vmaKmh float64) map[string]float64 {
	paces := make([]float64, len(in.Reps))
	for i, r := range in.Reps {
		paces[i] = r.pace()
	}
	mid := median(paces)
	floorPace := 3600 / (vmaKmh * intervalEffortFloor)
	res.ZoneSlowPaceSecPerKm = math.Round(floorPace)

	var sumReg, sumEffort, sumAbsDev float64
	res.Reps = make([]RepScore, 0, len(in.Reps))
	for i, r := range in.Reps {
		dev := paces[i] - mid
		regPts := curvePoints(dev, repFalloffSec, kmFalloffPower)
		effortPts := 100.0
		if paces[i] > floorPace {
			effortPts = curvePoints(paces[i]-floorPace, effortFalloffSec, kmFalloffPower)
		} else {
			res.KmsInBand++
		}
		rec := 0.0
		if i < len(in.RecoveryAfter) {
			rec = math.Round(in.RecoveryAfter[i])
		}
		res.Reps = append(res.Reps, RepScore{
			Rep:          i + 1,
			DistanceM:    math.Round(r.DistanceM),
			Sec:          math.Round(r.Sec),
			PaceSecPerKm: math.Round(paces[i]),
			DeltaSec:     math.Round(dev),
			Points:       math.Round(regPts*effortPts/100*10) / 10,
			RecoverySec:  rec,
			HrBpm:        math.Round(r.HrBpm),
		})
		sumReg += regPts
		sumEffort += effortPts
		sumAbsDev += math.Abs(dev)
	}
	n := float64(len(in.Reps))
	points := map[string]float64{
		"reps":   sumReg / n,
		"effort": sumEffort / n,
	}
	res.MeanAbsDevSec = math.Round(sumAbsDev/n*10) / 10

	// Tenue : dernier tiers contre premier tiers. Finir plus vite ne coûte
	// rien ; c'est le relâchement qui trahit des efforts mal dosés.
	third := len(paces) / 3
	if third < 1 {
		third = 1
	}
	first, last := mean(paces[:third]), mean(paces[len(paces)-third:])
	res.DriftSec = math.Round(last - first)
	points["fade"] = curvePoints(math.Max(0, last-first), fadeFalloffSec, kmFalloffPower)

	minPace, maxPace := math.Inf(1), math.Inf(-1)
	for i, p := range paces {
		if p < minPace {
			minPace, res.FastestKm = p, i+1
		}
		if p > maxPace {
			maxPace, res.SlowestKm = p, i+1
		}
	}
	res.SpreadSec = math.Round(maxPace - minPace)

	var recs []float64
	for _, r := range in.RecoveryAfter {
		if r > 0 {
			recs = append(recs, r)
		}
	}
	if len(recs) >= 2 {
		mr := median(recs)
		var dev float64
		for _, r := range recs {
			dev += math.Abs(r - mr)
		}
		pct := dev / float64(len(recs)) / mr * 100
		points["recoveries"] = curvePoints(pct, recoveryFalloffPct, kmFalloffPower)
	}

	res.Regularity = int(math.Round(points["reps"]))
	res.Chrono = int(math.Round(points["effort"]))
	return points
}

// intervalEffortCap : plafond de la note quand les efforts sont trop lents.
// Des répétitions courues en endurance ne font pas un fractionné, si
// régulières soient-elles : sans ce plafond, la régularité suffirait à
// décrocher une bonne note. Efforts au seuil → aucun plafond ; 10 s/km trop
// lents → 74 ; 20 s/km → 56.
func intervalEffortCap(effort float64) float64 {
	return 50 + effort/2
}

func mean(xs []float64) float64 {
	var s float64
	for _, x := range xs {
		s += x
	}
	return s / float64(len(xs))
}
