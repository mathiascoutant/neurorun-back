package vma

import (
	"math"
	"sort"
)

// ScoreVersion : version du barème. Une note enregistrée avec une version
// antérieure est recalculée à sa prochaine consultation.
//   - 1 : toute course comparée à l'allure de course prédite par la VMA.
//   - 2 : la course est d'abord classée (endurance, allure soutenue, course à
//     fond) puis jugée sur ce qu'elle cherchait à faire, cardio compris.
//   - 3 : le type de séance est choisi au départ (libre, endurance,
//     fractionné), le fractionné est noté sur ses répétitions, et le cardio se
//     juge sur la FC atteinte au test de VMA plutôt que sur l'âge.
//   - 4 : la météo de la course (chaleur, froid, vent) ralentit les allures
//     visées ; les courses déjà notées sont rejugées avec la leur.
const ScoreVersion = 4

// Types de séance. La note ne juge pas une sortie en endurance fondamentale
// comme un 10 km à fond : on vérifie qu'elle a été courue comme une endurance.
const (
	SessionEndurance = "endurance"
	SessionTempo     = "tempo"
	SessionRace      = "race"
	// Fractionné : jamais déduit de l'allure, seulement choisi au départ.
	SessionInterval = "interval"
)

// Réglages de la notation. Ils fixent à eux seuls la difficulté de l'échelle :
// une note au-dessus de 95 doit rester rare, une bonne sortie tourne entre
// 75 et 90 — quel que soit le type de séance.
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

	// Hors course à fond, la régularité se mesure à sa propre allure moyenne,
	// pas à un objectif extérieur : l'écart y est mécaniquement plus faible,
	// la pente est donc plus raide pour garder la même exigence.
	// Repères : 2 s → 96, 5 s → 84, 9 s → 66, 12 s → 52.
	steadyFalloffSec = 16.0

	// Course à fond : la régularité pèse plus lourd que le chrono, un temps
	// final juste obtenu en alternant les à-coups vaut moins qu'une course
	// tenue.
	weightRegularity = 0.70
	weightChrono     = 0.30

	// Distance minimale pour qu'une note ait du sens.
	MinScorableKm = 1.0

	// Au-delà de cet écart en avance sur l'objectif, la VMA de référence est
	// probablement dépassée : on invite à refaire le test.
	staleVmaPct = 3.0
)

// Classement de la séance et zones d'allure, en part de la VMA.
const (
	// Une course est jugée « à fond » dès que l'allure moyenne approche celle
	// que la VMA rend tenable sur la distance : à 2,5 points de VMA près, soit
	// environ 8 s/km sur un 10 km à VMA 15. Plus lent, c'est une autre séance.
	raceMargin = 0.025

	// Sous ce seuil, la sortie est une endurance ; au-dessus, une allure
	// soutenue (allure marathon, tempo, seuil).
	tempoFloor = 0.78

	// Zone d'endurance fondamentale. Le haut de la zone est la faute classique
	// (courir son footing trop vite) et coûte plein tarif ; le bas ne coûte
	// qu'à moitié, un footing très lent reste un footing.
	enduranceZoneLow  = 0.60
	enduranceZoneHigh = 0.76
)

// Cardio. La référence est la FC atteinte à la fin du test de VMA ; sans test
// mesuré au cardio, une FC max estimée par l'âge (Tanaka), relevée si la course
// montre plus haut. Les plafonds sont les bornes des zones affichées dans
// l'app : l'endurance doit rester en zone 2, l'allure soutenue sous la zone 5.
const (
	enduranceHrCeiling = HrZone3Floor
	tempoHrCeiling     = HrZone5Floor
	// Au-dessus du plafond : 3 bpm → 85, 5 bpm → 70, 10 bpm → 37.
	hrFalloffBpm = 10.0
	// Le cœur met quelques minutes à rejoindre l'effort : on ne juge pas le
	// début, ni les pics d'un capteur optique qui s'accroche à la cadence.
	hrWarmupMs = 180_000
	// Écart maximal compté entre deux échantillons : au-delà, le capteur a
	// décroché et le trou ne doit pas peser.
	hrMaxGapMs = 10_000

	// Dérive cardiaque (découplage allure/FC entre les deux moitiés) :
	// 1 % → 98, 2,5 % → 92, 5 % → 74, 10 % → 37. Sous 5 %, l'endurance
	// aérobie est solide ; au-delà, le cœur a payé la fin de sortie.
	decouplingFalloffPct = 10.0
	decouplingPower      = 1.75
	// Kilomètres avec FC nécessaires pour la mesurer (premier km exclu).
	minDecouplingKms = 3

	// Par forte chaleur, le cœur dérive même sur une sortie bien menée : on
	// tolère la moitié du ralentissement dû à la chaleur (5 % de chaleur →
	// 2,5 points de dérive offerts).
	heatDecouplingShare = 0.5
)

// Poids des composantes hors course à fond. Une composante absente (pas de
// cardio, course trop courte pour la dérive) est retirée et les autres
// reprennent sa part.
var steadyWeights = map[string]map[string]float64{
	// En endurance, c'est le cœur qui dit si l'allure était la bonne : le
	// cardio et sa tenue pèsent plus que l'allure au kilomètre.
	SessionEndurance: {"hr_zone": 35, "pace_zone": 10, "regularity": 25, "decoupling": 30},
	// En allure soutenue, c'est la tenue de l'allure qui fait la séance. La
	// zone d'allure ne pèse presque rien : la séance y a été classée d'après
	// son allure moyenne, elle y est donc presque toujours.
	SessionTempo: {"hr_zone": 20, "pace_zone": 10, "regularity": 45, "decoupling": 25},
}

var componentLabels = map[string]string{
	"regularity": "Régularité",
	"chrono":     "Chrono",
	"pace_zone":  "Allure",
	"hr_zone":    "Zone cardio",
	"decoupling": "Dérive cardiaque",
	"reps":       "Régularité des répétitions",
	"fade":       "Tenue des répétitions",
	"effort":     "Intensité des efforts",
	"recoveries": "Récupérations",
}

// componentOrder : ordre d'affichage des composantes.
var componentOrder = []string{
	"hr_zone", "pace_zone", "regularity", "decoupling", "chrono",
	"reps", "fade", "effort", "recoveries",
}

// Split : un kilomètre complet tel que remonté par l'app ou la montre.
type Split struct {
	Km           int
	PaceSecPerKm float64
	// EndMs : horodatage de fin du kilomètre, pour y rattacher la FC.
	EndMs int64
}

// HrSample : une mesure de fréquence cardiaque horodatée.
type HrSample struct {
	TMs int64
	Bpm float64
}

// Input : tout ce que la note sait d'une course.
type Input struct {
	Splits     []Split
	DistanceKm float64
	MovingSec  float64
	VmaKmh     float64

	// StartMs : début de la course, borne gauche du premier kilomètre.
	StartMs int64
	Hr      []HrSample
	// HrMaxBpm : FC de référence (0 = inconnue : pas de zone cardio).
	HrMaxBpm float64
	// HrMaxMeasured : la référence vient du test de VMA. Elle fait foi telle
	// quelle ; une estimation par l'âge, elle, est relevée si la course monte
	// plus haut.
	HrMaxMeasured bool
	// Intervals : découpage effort / récupération d'un fractionné.
	Intervals *IntervalInput

	// SessionType force le type de séance ; vide = détection par l'allure.
	SessionType string

	// HeatPct : ralentissement dû à la chaleur, en % (0 = temps frais ou météo
	// inconnue). Il sert aussi à tolérer la dérive cardiaque.
	HeatPct float64
	// WeatherPct : ralentissement total dû à la météo (chaleur, froid, vent),
	// en %. La course est jugée sur la VMA que la météo laisse. Zéro : on s'en
	// tient à HeatPct.
	WeatherPct float64
}

// KmScore : détail d'un kilomètre dans la note.
type KmScore struct {
	Km           int     `json:"km"`
	PaceSecPerKm float64 `json:"pace_sec_per_km"`
	// DeltaSec : écart signé à l'allure visée (négatif = plus rapide). Hors
	// course à fond, écart à la zone : 0 tant que le km y reste.
	DeltaSec float64 `json:"delta_sec"`
	Points   float64 `json:"points"`
	InZone   bool    `json:"in_zone"`
	// HrBpm : FC moyenne sur ce kilomètre (0 = pas de mesure).
	HrBpm float64 `json:"hr_bpm,omitempty"`
}

// Component : une composante de la note et son poids dans le total.
type Component struct {
	Key    string `json:"key"`
	Label  string `json:"label"`
	Score  int    `json:"score"`
	Weight int    `json:"weight"` // en %
}

// Result : note complète d'une course.
type Result struct {
	Scorable bool   `json:"scorable"`
	Reason   string `json:"reason,omitempty"` // pourquoi non notable

	Version     int    `json:"version"`
	SessionType string `json:"session_type"`
	// SessionAuto : le type a été déduit de l'allure, pas choisi.
	SessionAuto bool `json:"session_auto"`
	// IntervalsUndetected : fractionné choisi au départ, mais les répétitions
	// ne se détachent pas de la trace ; la sortie est jugée comme une course
	// continue.
	IntervalsUndetected bool `json:"intervals_undetected,omitempty"`

	Total int `json:"total"`
	// Regularity et Chrono restent remplis pour les versions de l'app qui ne
	// lisent pas Components : hors course à fond, Chrono porte l'allure.
	Regularity int         `json:"regularity"`
	Chrono     int         `json:"chrono"`
	Components []Component `json:"components"`
	Band       string      `json:"band"`

	VmaKmh     float64 `json:"vma_kmh"`
	DistanceKm float64 `json:"distance_km"`
	// IntensityPct : allure moyenne en % de la VMA.
	IntensityPct float64 `json:"intensity_pct"`
	// Allure de course prédite par la VMA : l'objectif d'une course à fond, et
	// un simple repère pour les autres séances.
	TargetPaceSecPerKm float64 `json:"target_pace_sec_per_km"`
	TargetTimeSec      float64 `json:"target_time_sec"`
	ActualTimeSec      float64 `json:"actual_time_sec"`
	// ChronoDeltaSec : écart signé au temps visé (négatif = plus rapide).
	ChronoDeltaSec float64 `json:"chrono_delta_sec"`
	// Zone d'allure de la séance (hors course à fond), en s/km.
	ZoneFastPaceSecPerKm float64 `json:"zone_fast_pace_sec_per_km,omitempty"`
	ZoneSlowPaceSecPerKm float64 `json:"zone_slow_pace_sec_per_km,omitempty"`

	Kms []KmScore `json:"kms"`
	// Reps : répétitions d'un fractionné, dans l'ordre de la séance.
	Reps []RepScore `json:"reps,omitempty"`

	// Indicateurs de lecture, réutilisés pour l'explication.
	KmsInBand     int     `json:"kms_in_band"`
	BestKm        int     `json:"best_km"`
	WorstKm       int     `json:"worst_km"`
	FastestKm     int     `json:"fastest_km"`
	SlowestKm     int     `json:"slowest_km"`
	SpreadSec     float64 `json:"spread_sec"`       // écart max-min entre kms
	MeanAbsDevSec float64 `json:"mean_abs_dev_sec"` // écart moyen (objectif, ou allure moyenne hors course)
	DriftSec      float64 `json:"drift_sec"`        // 2e moitié - 1re moitié
	StaleVmaHint  bool    `json:"stale_vma_hint"`

	// Cardio (0 / nil = pas de mesure exploitable).
	HrAvgBpm     float64 `json:"hr_avg_bpm,omitempty"`
	HrMaxBpm     float64 `json:"hr_max_bpm,omitempty"`
	HrCeilingBpm float64 `json:"hr_ceiling_bpm,omitempty"`
	// HrInZonePct : part du temps passé sous le plafond cardio.
	HrInZonePct   *float64 `json:"hr_in_zone_pct,omitempty"`
	DecouplingPct *float64 `json:"decoupling_pct,omitempty"`

	// HeatPct : correction de chaleur appliquée, en %. Les allures cibles et
	// les zones affichées en tiennent déjà compte ; VmaKmh reste la VMA du test.
	HeatPct float64 `json:"heat_pct,omitempty"`
	// WeatherPct : correction météo totale appliquée aux allures (chaleur,
	// froid, vent), en %.
	WeatherPct float64 `json:"weather_pct,omitempty"`
}

// curvePoints : 100 · exp(-(x / falloff) ^ power), 100 pour un écart nul.
func curvePoints(x, falloff, power float64) float64 {
	x = math.Abs(x)
	if x == 0 {
		return 100
	}
	return 100 * math.Exp(-math.Pow(x/falloff, power))
}

// deviationPoints note un écart à l'allure visée, exprimé en secondes par
// kilomètre. Même courbe pour la régularité, le chrono et la zone : tous
// parlent la même unité, donc l'échelle se comporte à l'identique que l'on
// coure à 3:30 ou à 6:30 au kilomètre.
func deviationPoints(devSecPerKm float64) float64 {
	return curvePoints(devSecPerKm, kmFalloffSec, kmFalloffPower)
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

// DetectSession classe une course d'après son allure moyenne, en part de la
// VMA : proche de l'allure de course prédite → course à fond ; sinon allure
// soutenue au-dessus de 78 %, endurance en dessous.
func DetectSession(intensity, distanceKm float64) string {
	if intensity >= FractionOfVma(distanceKm)-raceMargin {
		return SessionRace
	}
	if intensity >= tempoFloor {
		return SessionTempo
	}
	return SessionEndurance
}

// ValidSessionType dit si un type de séance est connu.
func ValidSessionType(t string) bool {
	return t == SessionEndurance || t == SessionTempo || t == SessionRace || t == SessionInterval
}

// Score note une course sur 100 selon ce qu'elle cherchait à faire.
//
//   - Course à fond : chaque kilomètre est comparé à l'allure que la VMA rend
//     tenable sur la distance (régularité 70 %), plus l'écart au chrono final
//     (30 %).
//   - Endurance, allure soutenue : on vérifie que les kilomètres sont restés
//     dans la zone d'allure de la séance, que l'allure a été tenue, que le
//     cœur est resté sous son plafond et qu'il n'a pas dérivé sur la fin.
//     Le chrono ne compte pas : un footing n'a pas à approcher l'allure de
//     course.
func Score(in Input) Result {
	res := Result{
		Version:       ScoreVersion,
		VmaKmh:        in.VmaKmh,
		DistanceKm:    in.DistanceKm,
		ActualTimeSec: in.MovingSec,
	}

	if in.VmaKmh <= 0 {
		res.Reason = "vma_absente"
		return res
	}
	if in.DistanceKm < MinScorableKm {
		res.Reason = "distance_trop_courte"
		return res
	}
	if in.MovingSec <= 0 {
		res.Reason = "duree_invalide"
		return res
	}

	// Par 29 °C et 70 % d'humidité, tenir 5:05 coûte ce que coûterait 5:22 au
	// frais ; un vent fort ou un froid vif ralentissent aussi : toutes les
	// cibles se lisent sur la VMA disponible ce jour-là.
	vmaKmh := in.VmaKmh
	slow := in.WeatherPct
	if slow <= 0 {
		slow = in.HeatPct
	}
	if in.HeatPct > 0 {
		res.HeatPct = in.HeatPct
	}
	if slow > 0 {
		res.WeatherPct = slow
		vmaKmh = in.VmaKmh / (1 + slow/100)
	}

	target := TargetFor(vmaKmh, in.DistanceKm)
	res.TargetPaceSecPerKm = target.PaceSecPerKm
	res.TargetTimeSec = target.TimeSec
	chronoDelta := in.MovingSec - target.TimeSec
	res.ChronoDeltaSec = math.Round(chronoDelta)

	// On ne note que les kilomètres complets : le dernier tronçon partiel a une
	// allure trop bruitée pour être comparée.
	complete := make([]Split, 0, len(in.Splits))
	for _, s := range in.Splits {
		if s.PaceSecPerKm > 0 && float64(s.Km) <= math.Floor(in.DistanceKm)+0.001 {
			complete = append(complete, s)
		}
	}
	sort.Slice(complete, func(i, j int) bool { return complete[i].Km < complete[j].Km })
	if len(complete) == 0 {
		res.Reason = "aucun_km_complet"
		return res
	}

	intensity := in.DistanceKm / (in.MovingSec / 3600) / vmaKmh
	res.IntensityPct = math.Round(intensity*1000) / 10
	res.SessionType, res.SessionAuto = in.SessionType, false
	if res.SessionType == SessionInterval && !in.Intervals.usable() {
		// Sans répétitions repérables, juger un fractionné sur elles n'aurait
		// aucun sens : on retombe sur la lecture d'une sortie continue.
		res.SessionType, res.IntervalsUndetected = "", true
	}
	if !ValidSessionType(res.SessionType) {
		res.SessionType, res.SessionAuto = DetectSession(intensity, in.DistanceKm), true
	}

	hr := analyzeHr(in, complete)

	var points map[string]float64
	if res.SessionType == SessionInterval {
		points = scoreIntervals(&res, in.Intervals, vmaKmh)
	} else if res.SessionType == SessionRace {
		points = scoreRace(&res, complete, target, chronoDelta, in.DistanceKm)
		// Nettement plus rapide que prévu : la VMA de référence a vieilli. Ce
		// repère-là reste relatif — 3 % d'avance ne pèsent pas pareil sur 5 km
		// et sur un marathon.
		res.StaleVmaHint = chronoDelta/target.TimeSec*100 < -staleVmaPct
	} else {
		points = scoreSteady(&res, complete, hr, vmaKmh, in.DistanceKm)
	}

	paceStats(&res, complete)
	for i := range res.Kms {
		res.Kms[i].HrBpm = hr.kmBpm[res.Kms[i].Km]
	}
	if hr.usable {
		res.HrAvgBpm = math.Round(hr.avg)
		if hr.maxBpm > 0 {
			res.HrMaxBpm = math.Round(hr.maxBpm)
		}
	}

	weights := map[string]float64{"regularity": weightRegularity * 100, "chrono": weightChrono * 100}
	switch res.SessionType {
	case SessionInterval:
		weights = intervalWeights
	case SessionEndurance, SessionTempo:
		weights = steadyWeights[res.SessionType]
	}
	var total float64
	res.Components, total = combine(points, weights)
	if res.SessionType == SessionInterval {
		total = math.Min(total, intervalEffortCap(points["effort"]))
	}

	res.Scorable = true
	res.Total = int(math.Round(total))
	res.Band = bandFor(res.Total)
	return res
}

// scoreRace : la course à fond, comparée kilomètre par kilomètre à l'allure
// prédite par la VMA, puis au chrono final.
func scoreRace(res *Result, complete []Split, target Target, chronoDelta, distanceKm float64) map[string]float64 {
	var sumPoints, sumAbsDev float64
	res.Kms = make([]KmScore, 0, len(complete))
	for _, s := range complete {
		delta := s.PaceSecPerKm - target.PaceSecPerKm
		pts := deviationPoints(delta)
		inBand := math.Abs(delta) <= kmToleranceSec
		res.Kms = append(res.Kms, KmScore{
			Km:           s.Km,
			PaceSecPerKm: s.PaceSecPerKm,
			DeltaSec:     math.Round(delta),
			Points:       math.Round(pts*10) / 10,
			InZone:       inBand,
		})
		sumPoints += pts
		sumAbsDev += math.Abs(delta)
		if inBand {
			res.KmsInBand++
		}
	}
	n := float64(len(complete))
	regularity := sumPoints / n
	res.MeanAbsDevSec = math.Round(sumAbsDev/n*10) / 10

	// Ramené en secondes par kilomètre : « j'ai perdu 4 s/km sur l'ensemble »
	// se compare directement aux écarts kilomètre par kilomètre.
	chrono := deviationPoints(chronoDelta / distanceKm)

	res.Regularity = int(math.Round(regularity))
	res.Chrono = int(math.Round(chrono))
	return map[string]float64{"regularity": regularity, "chrono": chrono}
}

// scoreSteady : endurance ou allure soutenue. Rien n'oblige à approcher
// l'allure de course ; on juge le respect de la zone, la tenue de l'allure et,
// quand la montre l'a mesuré, le cœur.
func scoreSteady(res *Result, complete []Split, hr hrAnalysis, vmaKmh, distanceKm float64) map[string]float64 {
	low, high, hrCeiling := enduranceZoneLow, enduranceZoneHigh, enduranceHrCeiling
	if res.SessionType == SessionTempo {
		// Jusqu'à l'allure de course : un kilomètre plus vite dans un tempo
		// reste un kilomètre de tempo.
		low, high, hrCeiling = tempoFloor, math.Max(FractionOfVma(distanceKm), tempoFloor), tempoHrCeiling
	}
	fastPace := 3600 / (vmaKmh * high)
	slowPace := 3600 / (vmaKmh * low)
	res.ZoneFastPaceSecPerKm = math.Round(fastPace)
	res.ZoneSlowPaceSecPerKm = math.Round(slowPace)

	var meanPace float64
	for _, s := range complete {
		meanPace += s.PaceSecPerKm
	}
	meanPace /= float64(len(complete))

	var sumZone, sumReg, sumAbsDev float64
	res.Kms = make([]KmScore, 0, len(complete))
	for _, s := range complete {
		var delta, zonePts float64
		switch {
		case s.PaceSecPerKm < fastPace:
			delta = s.PaceSecPerKm - fastPace
			zonePts = deviationPoints(delta)
		case s.PaceSecPerKm > slowPace:
			delta = s.PaceSecPerKm - slowPace
			zonePts = deviationPoints(delta)
			if res.SessionType == SessionEndurance {
				zonePts = deviationPoints(delta / 2)
			}
		default:
			zonePts = 100
		}
		dev := s.PaceSecPerKm - meanPace
		regPts := curvePoints(dev, steadyFalloffSec, kmFalloffPower)
		inZone := delta == 0
		res.Kms = append(res.Kms, KmScore{
			Km:           s.Km,
			PaceSecPerKm: s.PaceSecPerKm,
			DeltaSec:     math.Round(delta),
			Points:       math.Round(zonePts*regPts/100*10) / 10,
			InZone:       inZone,
		})
		sumZone += zonePts
		sumReg += regPts
		sumAbsDev += math.Abs(dev)
		if inZone {
			res.KmsInBand++
		}
	}
	n := float64(len(complete))
	points := map[string]float64{
		"pace_zone":  sumZone / n,
		"regularity": sumReg / n,
	}
	res.MeanAbsDevSec = math.Round(sumAbsDev/n*10) / 10

	if hr.usable && hr.maxBpm > 0 {
		ceiling := hrCeiling * hr.maxBpm
		pts, inZone := hr.zone(ceiling)
		points["hr_zone"] = pts
		res.HrCeilingBpm = math.Round(ceiling)
		inZonePct := math.Round(inZone*1000) / 10
		res.HrInZonePct = &inZonePct
	}
	if hr.decoupling != nil {
		judged := *hr.decoupling - res.HeatPct*heatDecouplingShare
		points["decoupling"] = curvePoints(math.Max(0, judged), decouplingFalloffPct, decouplingPower)
		d := math.Round(*hr.decoupling*10) / 10
		res.DecouplingPct = &d
	}

	res.Regularity = int(math.Round(points["regularity"]))
	res.Chrono = int(math.Round(points["pace_zone"]))
	return points
}

// paceStats : repères de lecture communs à tous les types de séance.
func paceStats(res *Result, complete []Split) {
	// Fractionné : ses repères se lisent sur les répétitions, pas au km.
	if len(res.Kms) == 0 {
		return
	}
	bestPoints, worstPoints := -1.0, 101.0
	minPace, maxPace := math.Inf(1), math.Inf(-1)
	for _, k := range res.Kms {
		if k.Points > bestPoints {
			bestPoints, res.BestKm = k.Points, k.Km
		}
		if k.Points < worstPoints {
			worstPoints, res.WorstKm = k.Points, k.Km
		}
		if k.PaceSecPerKm < minPace {
			minPace, res.FastestKm = k.PaceSecPerKm, k.Km
		}
		if k.PaceSecPerKm > maxPace {
			maxPace, res.SlowestKm = k.PaceSecPerKm, k.Km
		}
	}
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
}

// combine pondère les composantes présentes. Les poids des absentes sont
// redistribués au prorata, et les pourcentages affichés tombent juste à 100.
func combine(points, weights map[string]float64) ([]Component, float64) {
	var sumW float64
	for key, w := range weights {
		if _, ok := points[key]; ok {
			sumW += w
		}
	}
	if sumW == 0 {
		return nil, 0
	}

	var total float64
	comps := make([]Component, 0, len(points))
	shares := make([]float64, 0, len(points))
	for _, key := range componentOrder {
		pts, ok := points[key]
		w, weighted := weights[key]
		if !ok || !weighted {
			continue
		}
		share := w / sumW
		total += share * pts
		comps = append(comps, Component{
			Key:    key,
			Label:  componentLabels[key],
			Score:  int(math.Round(pts)),
			Weight: int(math.Floor(share * 100)),
		})
		shares = append(shares, share*100)
	}

	// Plus forts restes : 37,5 / 62,5 s'affichent 38 / 62, pas 37 / 62.
	missing := 100
	for _, c := range comps {
		missing -= c.Weight
	}
	order := make([]int, len(comps))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		ra := shares[order[a]] - math.Floor(shares[order[a]])
		rb := shares[order[b]] - math.Floor(shares[order[b]])
		return ra > rb
	})
	for i := 0; i < missing && i < len(order); i++ {
		comps[order[i]].Weight++
	}
	return comps, total
}

// hrAnalysis : ce que la FC dit de la course.
type hrAnalysis struct {
	usable  bool
	samples []HrSample // nettoyés, triés
	avg     float64
	maxBpm  float64 // FC max retenue (estimée, relevée par la course)
	// kmBpm : FC moyenne par kilomètre complet.
	kmBpm      map[int]float64
	decoupling *float64 // en %, positif = le cœur a dérivé
}

// analyzeHr nettoie la FC, la rattache aux kilomètres et mesure sa dérive.
func analyzeHr(in Input, complete []Split) hrAnalysis {
	a := hrAnalysis{kmBpm: map[int]float64{}}

	for _, s := range in.Hr {
		if s.Bpm >= 35 && s.Bpm <= 230 && s.TMs > 0 {
			a.samples = append(a.samples, s)
		}
	}
	sort.Slice(a.samples, func(i, j int) bool { return a.samples[i].TMs < a.samples[j].TMs })
	// Exploitable si la FC couvre au moins la moitié de la course.
	if len(a.samples) < 30 {
		return a
	}
	span := float64(a.samples[len(a.samples)-1].TMs-a.samples[0].TMs) / 1000
	if span < 0.5*in.MovingSec {
		return a
	}
	a.usable = true

	var sumW, sumBpm float64
	for i, s := range a.samples {
		w := a.weight(i)
		sumW += w
		sumBpm += w * s.Bpm
	}
	a.avg = sumBpm / sumW

	// FC de référence : celle du test de VMA fait foi. Une estimation par
	// l'âge, elle, cède si la course est montée plus haut (98e centile, pour
	// ignorer les pics isolés du capteur).
	if in.HrMaxMeasured {
		a.maxBpm = in.HrMaxBpm
	} else if in.HrMaxBpm > 0 {
		sorted := make([]float64, len(a.samples))
		for i, s := range a.samples {
			sorted[i] = s.Bpm
		}
		sort.Float64s(sorted)
		peak := sorted[int(math.Floor(0.98*float64(len(sorted)-1)))]
		a.maxBpm = math.Max(in.HrMaxBpm, peak)
	}

	// FC par kilomètre : échantillons entre la fin du km précédent (le départ
	// pour le premier) et la fin de celui-ci.
	endOf := map[int]int64{0: in.StartMs}
	for _, s := range in.Splits {
		endOf[s.Km] = s.EndMs
	}
	for _, s := range complete {
		start := endOf[s.Km-1]
		if s.EndMs <= 0 || start <= 0 || s.EndMs <= start {
			continue
		}
		var sum float64
		var n int
		for _, h := range a.samples {
			if h.TMs > start && h.TMs <= s.EndMs {
				sum += h.Bpm
				n++
			}
		}
		if n >= 3 {
			a.kmBpm[s.Km] = math.Round(sum / float64(n))
		}
	}

	a.decoupling = decoupling(complete, a.kmBpm)
	return a
}

// weight : durée représentée par l'échantillon i, trous du capteur exclus.
func (a hrAnalysis) weight(i int) float64 {
	if i+1 < len(a.samples) {
		dt := a.samples[i+1].TMs - a.samples[i].TMs
		if dt > hrMaxGapMs {
			dt = hrMaxGapMs
		}
		return float64(dt)
	}
	return 1000
}

// zone : points de la zone cardio et part du temps sous le plafond, sans
// compter l'échauffement.
func (a hrAnalysis) zone(ceiling float64) (points, inZoneShare float64) {
	from := a.samples[0].TMs + hrWarmupMs
	var sumW, sumPts, inW float64
	for i, s := range a.samples {
		if s.TMs < from {
			continue
		}
		w := a.weight(i)
		sumW += w
		if s.Bpm <= ceiling {
			sumPts += w * 100
			inW += w
		} else {
			sumPts += w * curvePoints(s.Bpm-ceiling, hrFalloffBpm, 1.5)
		}
	}
	if sumW == 0 {
		return 100, 1
	}
	return sumPts / sumW, inW / sumW
}

// decoupling : perte d'efficacité (vitesse par battement) entre la première
// et la seconde moitié. Le premier kilomètre est écarté : le cœur n'y a pas
// encore rejoint l'effort, il gonflerait l'efficacité du début.
func decoupling(complete []Split, kmBpm map[int]float64) *float64 {
	type km struct{ speed, bpm float64 }
	kms := make([]km, 0, len(complete))
	judged := 0
	for _, s := range complete {
		if s.Km == 1 {
			continue
		}
		judged++
		if bpm, ok := kmBpm[s.Km]; ok {
			kms = append(kms, km{3600 / s.PaceSecPerKm, bpm})
		}
	}
	// Trop de kilomètres sans FC : les deux moitiés ne se comparent plus.
	if len(kms) < minDecouplingKms || 3*len(kms) < 2*judged {
		return nil
	}
	half := len(kms) / 2
	eff := func(part []km) float64 {
		var speed, bpm float64
		for _, k := range part {
			speed += k.speed
			bpm += k.bpm
		}
		return speed / bpm
	}
	first, last := eff(kms[:half]), eff(kms[len(kms)-half:])
	d := (first - last) / first * 100
	return &d
}
