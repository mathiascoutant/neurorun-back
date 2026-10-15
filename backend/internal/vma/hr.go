package vma

import (
	"math"
	"sort"
	"time"
)

// Zones cardiaques, en part de la FC de référence. La référence est la FC
// atteinte à la fin du test de VMA : six minutes à fond, c'est la FC que le
// coureur tient à sa VMA. La zone 5 est donc réservée à l'intensité VMA ;
// une FC élevée mais sous ce niveau reste du seuil, pas du maximal.
const (
	HrZone2Floor = 0.70 // endurance fondamentale
	HrZone3Floor = 0.80 // endurance active, tempo
	HrZone4Floor = 0.88 // seuil
	HrZone5Floor = 0.95 // VMA
)

// Sources de la FC de référence.
const (
	HrRefVmaTest = "vma_test"
	HrRefAge     = "age"
)

// Fenêtre de lissage du pic de FC d'un test : un capteur optique peut
// afficher un pic isolé (cadence prise pour le pouls) que le cœur n'a jamais
// atteint.
const testHrWindowMs = 10_000

// TestHrPeak : FC atteinte pendant un test de VMA, soit la plus haute moyenne
// sur 10 secondes. 0 si le test n'a pas été couru avec un cardio exploitable.
func TestHrPeak(samples []HrSample) float64 {
	clean := make([]HrSample, 0, len(samples))
	for _, s := range samples {
		if s.Bpm >= 35 && s.Bpm <= 230 && s.TMs > 0 {
			clean = append(clean, s)
		}
	}
	// Un test de 6 min échantillonné à la seconde en donne ~360 : en deçà
	// d'une minute de mesures, le capteur a décroché.
	if len(clean) < 60 {
		return 0
	}
	sort.Slice(clean, func(i, j int) bool { return clean[i].TMs < clean[j].TMs })

	var peak, sum float64
	lo := 0
	for hi := range clean {
		sum += clean[hi].Bpm
		for clean[hi].TMs-clean[lo].TMs > testHrWindowMs {
			sum -= clean[lo].Bpm
			lo++
		}
		if n := hi - lo + 1; n >= 3 {
			peak = math.Max(peak, sum/float64(n))
		}
	}
	// Un test à fond qui ne dépasse pas 120 bpm n'a pas été mesuré au poignet.
	if peak < 120 {
		return 0
	}
	return math.Round(peak)
}

// HrZoneBand : bornes d'une zone cardiaque, en bpm.
type HrZoneBand struct {
	Index   int     `json:"index"`
	LowBpm  float64 `json:"low_bpm"`
	HighBpm float64 `json:"high_bpm"`
}

// HrZones : les cinq zones pour une FC de référence. La zone 5 n'a pas de
// plafond physiologique : on la borne à 110 % pour l'affichage.
func HrZones(refBpm float64) []HrZoneBand {
	floors := []float64{0.50, HrZone2Floor, HrZone3Floor, HrZone4Floor, HrZone5Floor, 1.10}
	out := make([]HrZoneBand, 0, 5)
	for i := 0; i < 5; i++ {
		out = append(out, HrZoneBand{
			Index:   i + 1,
			LowBpm:  math.Round(floors[i] * refBpm),
			HighBpm: math.Round(floors[i+1] * refBpm),
		})
	}
	return out
}

// HrReference : la FC qui sert de base aux zones d'un coureur.
type HrReference struct {
	Bpm    float64 `json:"bpm"`
	Source string  `json:"source"` // HrRefVmaTest ou HrRefAge
	// TestedAt : date du test de VMA d'où vient la mesure.
	TestedAt *time.Time   `json:"tested_at,omitempty"`
	Zones    []HrZoneBand `json:"zones"`
}

// Measured dit si la référence vient d'un test plutôt que de l'âge.
func (r *HrReference) Measured() bool {
	return r != nil && r.Source == HrRefVmaTest
}
