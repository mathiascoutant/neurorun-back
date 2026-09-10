// Package vma : test de VMA (6 minutes) et notation d'une course sur 100.
//
// Deux briques indépendantes :
//   - la VMA, mesurée par un test de 6 minutes (demi-Cooper), qui sert de
//     référence pour prédire l'allure tenable sur n'importe quelle distance ;
//   - la note, entièrement déterministe : on compare chaque kilomètre à
//     l'allure prévue. L'IA n'intervient que pour rédiger l'explication.
package vma

import "math"

// TestDurationSec : durée officielle du test (6 minutes).
const TestDurationSec = 360.0

// Bornes de plausibilité d'une VMA humaine (km/h). Un GPS qui dérape ou un
// test fait en voiture ne doit pas s'enregistrer comme une VMA de 40.
const (
	MinVmaKmh = 6.0
	MaxVmaKmh = 26.0
)

// FromTest déduit la VMA d'un test de 6 minutes.
//
// Principe du demi-Cooper : la distance parcourue en 6 min à fond correspond
// à la distance couverte en une heure à VMA divisée par 10, soit
// VMA (km/h) = distance (m) / 100. Si le test n'a pas duré exactement
// 6 minutes (arrêt manuel un peu tardif), on ramène la distance au prorata
// plutôt que de fausser la mesure.
func FromTest(distanceM, durationSec float64) float64 {
	if distanceM <= 0 || durationSec <= 0 {
		return 0
	}
	normalized := distanceM * (TestDurationSec / durationSec)
	return normalized / 100
}

// PlausibleVma dit si une VMA mesurée est exploitable.
func PlausibleVma(vmaKmh float64) bool {
	return vmaKmh >= MinVmaKmh && vmaKmh <= MaxVmaKmh
}

// fractionAnchor : part de la VMA tenable sur une distance donnée.
// Au-delà de quelques minutes d'effort, l'allure soutenable décroît avec la
// distance — c'est la relation classique entre temps limite et % de VMA.
type fractionAnchor struct {
	km       float64
	fraction float64
}

var fractionTable = []fractionAnchor{
	{1, 1.05},
	{2, 1.00},
	{3, 0.97},
	{5, 0.935},
	{10, 0.885},
	{15, 0.855},
	{21.0975, 0.83},
	{30, 0.80},
	{42.195, 0.775},
}

// FractionOfVma : part de la VMA tenable sur distanceKm, interpolée
// linéairement en logarithme de la distance (l'endurance se dégrade par
// paliers multiplicatifs, pas additifs).
func FractionOfVma(distanceKm float64) float64 {
	if distanceKm <= 0 {
		return 0
	}
	first, last := fractionTable[0], fractionTable[len(fractionTable)-1]
	if distanceKm <= first.km {
		return first.fraction
	}
	if distanceKm >= last.km {
		return last.fraction
	}
	for i := 1; i < len(fractionTable); i++ {
		hi := fractionTable[i]
		if distanceKm > hi.km {
			continue
		}
		lo := fractionTable[i-1]
		t := (math.Log(distanceKm) - math.Log(lo.km)) / (math.Log(hi.km) - math.Log(lo.km))
		return lo.fraction + t*(hi.fraction-lo.fraction)
	}
	return last.fraction
}

// Target : objectif calculé pour une distance à partir de la VMA.
type Target struct {
	DistanceKm    float64 `json:"distance_km"`
	PaceSecPerKm  float64 `json:"pace_sec_per_km"`
	TimeSec       float64 `json:"time_sec"`
	SpeedKmh      float64 `json:"speed_kmh"`
	FractionOfVma float64 `json:"fraction_of_vma"`
}

// TargetFor : allure et temps visés sur une distance, pour une VMA donnée.
func TargetFor(vmaKmh, distanceKm float64) Target {
	if vmaKmh <= 0 || distanceKm <= 0 {
		return Target{}
	}
	frac := FractionOfVma(distanceKm)
	speed := vmaKmh * frac
	pace := 3600 / speed
	return Target{
		DistanceKm:    distanceKm,
		PaceSecPerKm:  math.Round(pace),
		TimeSec:       math.Round(pace * distanceKm),
		SpeedKmh:      speed,
		FractionOfVma: frac,
	}
}

// StandardDistances : distances proposées dans l'onglet Prévision.
var StandardDistances = []struct {
	ID string
	Km float64
}{
	{"1k", 1},
	{"5k", 5},
	{"10k", 10},
	{"half", 21.0975},
	{"marathon", 42.195},
}
