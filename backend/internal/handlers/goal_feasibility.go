package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"runapp/internal/goalcalendar"
	"runapp/internal/models"
	"strconv"
	"strings"
	"time"

	"runapp/internal/vma"
)

// Avis de faisabilité d'un objectif.
//
// Le verdict ne se devine pas : il se calcule. Laissé au modèle de langage
// face à cinquante activités brutes, il jugeait « peu réaliste » un 5 km en
// 54 min pour quelqu'un qui court déjà 10 km en 54 min — un chrono plus lent
// que le niveau actuel passait pour un échec. On mesure donc le niveau sur les
// sorties (même modèle VMA que l'onglet Prévision), on le compare au chrono
// visé, et le modèle ne fait plus que l'expliquer avec ces chiffres.

// Verdicts possibles, du plus confortable au plus tendu.
const (
	VerdictEasy      = "Largement à ta portée"
	VerdictRealistic = "Réaliste"
	VerdictStretch   = "Ambitieux mais jouable"
	VerdictTooHard   = "Très tendu / peu réaliste"
)

// FeasibilityRun : la sortie qui fixe le niveau mesuré.
type FeasibilityRun struct {
	Name       string  `json:"name"`
	Date       string  `json:"date"`
	DistanceKm float64 `json:"distance_km"`
	TimeSec    float64 `json:"time_sec"`
}

// Niveaux de fiabilité de l'estimation.
const (
	ReliabilityGood   = "bonne"
	ReliabilityMedium = "moyenne"
	ReliabilityLow    = "faible"
)

// FeasibilityFacts : ce que l'on sait avant d'écrire l'avis. Les champs à zéro
// sont inconnus (pas de chrono chiffré, pas de sortie exploitable).
type FeasibilityFacts struct {
	Verdict       string  `json:"verdict"`
	GoalKm        float64 `json:"goal_km"`
	TargetSec     float64 `json:"target_sec"`
	TargetPaceSec float64 `json:"target_pace_sec_per_km"`
	// PredictedSec : niveau actuel le plus probable, au milieu de la fourchette.
	PredictedSec     float64 `json:"predicted_sec"`
	PredictedPaceSec float64 `json:"predicted_pace_sec_per_km"`
	// FastSec / SlowSec : la fourchette du niveau actuel. FastSec suppose une
	// endurance déjà au point sur la distance ; SlowSec est ce que les sorties
	// permettent de tenir pour acquis. Une estimation tirée d'une sortie courte
	// donne une fourchette large : c'est voulu, on ne sait pas mieux.
	FastSec float64 `json:"fast_sec"`
	SlowSec float64 `json:"slow_sec"`
	// FastLabel / SlowLabel : la fourchette arrondie à ce qu'elle vaut vraiment.
	FastLabel string `json:"fast_label,omitempty"`
	SlowLabel string `json:"slow_label,omitempty"`
	// Reliability : bonne, moyenne ou faible ; ReliabilityWhy dit pourquoi.
	Reliability    string `json:"reliability,omitempty"`
	ReliabilityWhy string `json:"reliability_why,omitempty"`
	// GapPct : effort demandé par rapport au niveau actuel. Positif = le chrono
	// visé est plus rapide que ce que tu cours aujourd'hui.
	GapPct float64 `json:"gap_pct"`
	// AllowedPct : progression raisonnable sur la préparation choisie.
	AllowedPct   float64         `json:"allowed_pct"`
	Reference    *FeasibilityRun `json:"reference_run,omitempty"`
	LongestRunKm float64         `json:"longest_run_km"`
	// WeeklyKm : volume moyen des quatre dernières semaines ; 0 si inconnu.
	WeeklyKm float64 `json:"weekly_km"`
	// RecentRuns : sorties des 90 derniers jours prises en compte.
	RecentRuns int `json:"recent_runs"`
	// VmaKmh : VMA du dernier test passé dans l'app, 0 sans test récent.
	VmaKmh float64 `json:"vma_kmh,omitempty"`
	// Warnings : points d'attention calculés, à dire tels quels.
	Warnings     []string `json:"warnings,omitempty"`
	Trail        bool     `json:"trail"`
	RunsAnalyzed int      `json:"runs_analyzed"`
}

// VmaReference : le dernier test de VMA, mesure directe de la vitesse du coureur.
type VmaReference struct {
	Kmh float64
	At  time.Time
	// Km : distance couverte pendant le test.
	Km float64
}

var (
	reHourMin = regexp.MustCompile(`(\d{1,2})\s*h\s*(\d{1,2})?`)
	reHMS     = regexp.MustCompile(`(\d{1,2}):(\d{2}):(\d{2})`)
	reMS      = regexp.MustCompile(`(\d{1,3}):(\d{2})`)
	reMinutes = regexp.MustCompile(`(\d{1,3})(?:[.,](\d))?\s*(?:min|mn|m\b|'|$|\s)`)
)

// parseTargetSeconds lit un chrono saisi librement : « 54 min », « 54 »,
// « 1h40 », « 1 h », « 48:30 », « 1:45:00 », « moins de 4h ». false si le
// texte n'en contient pas (« finir sans chrono précis ») ou si la valeur est
// hors de toute course plausible.
func parseTargetSeconds(raw string) (float64, bool) {
	s := strings.ToLower(strings.TrimSpace(raw))
	if s == "" {
		return 0, false
	}
	var sec float64
	switch {
	case reHMS.MatchString(s):
		m := reHMS.FindStringSubmatch(s)
		h, _ := strconv.Atoi(m[1])
		mi, _ := strconv.Atoi(m[2])
		se, _ := strconv.Atoi(m[3])
		sec = float64(h*3600 + mi*60 + se)
	case reHourMin.MatchString(s):
		m := reHourMin.FindStringSubmatch(s)
		h, _ := strconv.Atoi(m[1])
		mi := 0
		if m[2] != "" {
			mi, _ = strconv.Atoi(m[2])
		}
		sec = float64(h*3600 + mi*60)
	case reMS.MatchString(s):
		m := reMS.FindStringSubmatch(s)
		mi, _ := strconv.Atoi(m[1])
		se, _ := strconv.Atoi(m[2])
		sec = float64(mi*60 + se)
	case reMinutes.MatchString(s):
		m := reMinutes.FindStringSubmatch(s)
		mi, _ := strconv.Atoi(m[1])
		sec = float64(mi * 60)
		if m[2] != "" {
			d, _ := strconv.Atoi(m[2])
			sec += float64(d) * 6
		}
	default:
		return 0, false
	}
	// Plus rapide que 10 min ou plus long que 8 h : ce n'est pas un chrono de course.
	if sec < 600 || sec > 8*3600 {
		return 0, false
	}
	return sec, true
}

// raceDistanceKm : distance officielle d'une épreuve, l'objectif la stockant
// arrondie (21, 42) — un marathon prédit sur 42 km serait trop court de 195 m.
func raceDistanceKm(km float64) float64 {
	switch km {
	case 21:
		return 21.0975
	case 42:
		return 42.195
	}
	return km
}

func actFloat(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case int:
		return float64(x)
	case int64:
		return float64(x)
	default:
		return 0
	}
}

func isRunType(v any) bool {
	t, _ := v.(string)
	switch t {
	case "Run", "TrailRun", "VirtualRun":
		return true
	}
	return false
}

// allowedProgress : progression raisonnable du chrono sur la préparation.
// Environ 0,8 % par semaine avec trois séances, modulé par la fréquence,
// borné entre 2 % (même courte, une prépa affûte) et 15 %.
func allowedProgress(weeks, sessionsPerWeek int) float64 {
	w := math.Min(float64(weeks), 16)
	f := math.Max(0.6, math.Min(1.3, float64(sessionsPerWeek)/3))
	return math.Max(0.02, math.Min(0.15, 0.008*w*f))
}

const (
	// feasibilityRecentDays : au-delà, une sortie ne dit plus le niveau d'aujourd'hui.
	feasibilityRecentDays = 90
	// enduranceDoubt : ce qu'on perd, au pire, à extrapoler vers plus long. Le
	// modèle suppose une endurance entraînée (exposant de Riegel proche de 1,08) ;
	// un coureur qui n'a jamais couru la distance se rapproche de 1,15. L'écart
	// entre les deux, appliqué au rapport des distances, borne la fourchette.
	enduranceDoubt = 0.07
)

// assessFeasibility : calcul sur les seules sorties, à la date du jour.
func assessFeasibility(acts []map[string]any, goalKm float64, targetTime string, weeks, sessionsPerWeek int, trail bool) FeasibilityFacts {
	return assessFeasibilityAt(acts, nil, goalKm, targetTime, weeks, sessionsPerWeek, trail, time.Now())
}

// assessFeasibilityAt calcule le niveau actuel et le compare au chrono visé.
//
// Le niveau est une fourchette, pas un chiffre :
//   - le bas (rapide) est ce que donnerait la vitesse mesurée — test de VMA ou
//     meilleure sortie — avec une endurance au point sur la distance ;
//   - le haut (lent) est ce que les sorties permettent de tenir pour acquis,
//     chacune étant d'autant moins probante qu'elle est courte devant la course.
//
// Une sortie proche de la distance visée resserre la fourchette ; trois
// kilomètres pour juger d'un marathon la laissent large. Le verdict se prend au
// milieu, et la fiabilité dit à quel point s'y fier.
//
// En trail, le dénivelé fausse la comparaison avec des allures sur route : les
// chiffres sont fournis au coach, mais le verdict ne lui est pas imposé.
func assessFeasibilityAt(
	acts []map[string]any, test *VmaReference,
	goalKm float64, targetTime string, weeks, sessionsPerWeek int, trail bool, now time.Time,
) FeasibilityFacts {
	goalKm = raceDistanceKm(goalKm)
	f := FeasibilityFacts{GoalKm: goalKm, AllowedPct: allowedProgress(weeks, sessionsPerWeek), Trail: trail}
	if sec, ok := parseTargetSeconds(targetTime); ok {
		f.TargetSec = sec
		f.TargetPaceSec = math.Round(sec / goalKm)
	}

	type candidate struct {
		vma    float64
		km     float64
		recent bool
		run    *FeasibilityRun
	}
	recentFrom := now.AddDate(0, 0, -feasibilityRecentDays)
	monthFrom := now.AddDate(0, 0, -28)
	var all []candidate
	var kmLast28, longestRecent, longestAny float64
	dated := false
	for _, a := range acts {
		if !isRunType(a["type"]) {
			continue
		}
		km := actFloat(a["distance_km"])
		min := actFloat(a["moving_time_min"])
		if km <= 0 || min <= 0 {
			continue
		}
		f.RunsAnalyzed++
		name, _ := a["name"].(string)
		date, _ := a["start_date"].(string)
		// Une sortie sans date est tenue pour récente : mieux vaut la compter que l'ignorer.
		recent := true
		if t, err := time.Parse(time.RFC3339, date); err == nil {
			dated = true
			recent = !t.Before(recentFrom)
			if !t.Before(monthFrom) {
				kmLast28 += km
			}
			date = t.Format("2006-01-02")
		}
		if km > longestAny {
			longestAny = km
		}
		if recent {
			f.RecentRuns++
			if km > longestRecent {
				longestRecent = km
			}
		}
		implied := (km / (min / 60)) / vma.FractionOfVma(km)
		if !vma.PlausibleVma(implied) {
			continue
		}
		all = append(all, candidate{implied, km, recent, &FeasibilityRun{Name: name, Date: date, DistanceKm: km, TimeSec: math.Round(min * 60)}})
	}
	if dated {
		f.WeeklyKm = math.Round(kmLast28/4*10) / 10
	}

	// Les sorties récentes font foi ; à défaut seulement, on remonte plus loin.
	stale := false
	var runs []candidate
	for _, c := range all {
		if c.recent {
			runs = append(runs, c)
		}
	}
	f.LongestRunKm = longestRecent
	if len(runs) == 0 && len(all) > 0 {
		runs, stale = all, true
		f.LongestRunKm = longestAny
	}
	if test != nil && vma.PlausibleVma(test.Kmh) && !test.At.Before(now.AddDate(0, 0, -2*feasibilityRecentDays)) {
		f.VmaKmh = math.Round(test.Kmh*10) / 10
		km := test.Km
		if km <= 0 {
			km = test.Kmh / 10 // six minutes à la VMA
		}
		runs = append(runs, candidate{vma: test.Kmh, km: km, recent: true})
	}

	// Plus une sortie est courte, plus l'extrapolation est optimiste : 1,5 km vite
	// ne dit rien de l'endurance sur 5 km. On ne descend sous 3 km que faute de mieux.
	usable := runs[:0:0]
	for _, minKm := range []float64{3, 1.5} {
		for _, c := range runs {
			if c.km >= minKm || c.run == nil {
				usable = append(usable, c)
			}
		}
		if len(usable) > 0 {
			break
		}
	}

	predict := func(v float64) float64 { return vma.TargetFor(v, goalKm).TimeSec }
	var ref *candidate
	for i := range usable {
		c := &usable[i]
		fast := predict(c.vma)
		slow := fast
		if c.km < goalKm {
			slow = fast * math.Pow(goalKm/c.km, enduranceDoubt)
		}
		if f.FastSec == 0 || fast < f.FastSec {
			f.FastSec = fast
		}
		if f.SlowSec == 0 || slow < f.SlowSec {
			f.SlowSec = slow
			ref = c
		}
	}
	if f.FastSec > f.SlowSec {
		f.FastSec = f.SlowSec
	}
	if ref != nil {
		f.Reference = ref.run
		f.PredictedSec = math.Round(math.Sqrt(f.FastSec * f.SlowSec))
		f.PredictedPaceSec = math.Round(f.PredictedSec / goalKm)
		f.FastSec, f.SlowSec = math.Round(f.FastSec), math.Round(f.SlowSec)
	}

	// Fiabilité : largeur de la fourchette, fraîcheur et nombre des sorties.
	if ref != nil {
		width := f.SlowSec/f.FastSec - 1
		switch {
		case stale:
			f.Reliability = ReliabilityLow
			f.ReliabilityWhy = "tes dernières sorties datent de plus de trois mois"
		case width > 0.10:
			f.Reliability = ReliabilityLow
			if ref.run != nil {
				f.ReliabilityWhy = fmt.Sprintf("ta sortie la plus probante fait %s, loin des %s de la course", kmFR(ref.run.DistanceKm), kmFR(goalKm))
			} else {
				f.ReliabilityWhy = fmt.Sprintf("elle repose sur ton test de VMA, sans sortie proche des %s de la course", kmFR(goalKm))
			}
		case width > 0.04 || f.RecentRuns < 4:
			f.Reliability = ReliabilityMedium
			if f.RecentRuns < 4 {
				f.ReliabilityWhy = fmt.Sprintf("elle repose sur %d sortie(s) récente(s) seulement", f.RecentRuns)
			} else if ref.run != nil {
				f.ReliabilityWhy = fmt.Sprintf("ta sortie la plus probante fait %s pour une course de %s", kmFR(ref.run.DistanceKm), kmFR(goalKm))
			} else {
				f.ReliabilityWhy = "elle repose surtout sur ton test de VMA"
			}
		default:
			f.Reliability = ReliabilityGood
			f.ReliabilityWhy = fmt.Sprintf("%d sorties récentes, dont une proche de la distance visée", f.RecentRuns)
		}
		f.FastLabel = approxDuration(f.FastSec, f.Reliability)
		f.SlowLabel = approxDuration(f.SlowSec, f.Reliability)
	}

	// Points d'attention : dits par le code, le coach ne fait que les reprendre.
	raceName := "cette distance"
	switch {
	case goalKm >= 42:
		raceName = "un marathon"
	case goalKm >= 21:
		raceName = "un semi-marathon"
	}
	if ref != nil && stale {
		f.Warnings = append(f.Warnings, "Tes dernières sorties ont plus de trois mois : ton niveau a pu changer depuis.")
	}
	if goalKm >= 21 && f.LongestRunKm > 0 && f.LongestRunKm < goalKm*0.4 {
		f.Warnings = append(f.Warnings, fmt.Sprintf(
			"Ta plus longue sortie récente fait %s : tenir la distance est le vrai chantier de cette préparation, bien avant la vitesse.", kmFR(f.LongestRunKm)))
	}
	if goalKm >= 21 && dated && f.WeeklyKm < goalKm*0.5 {
		f.Warnings = append(f.Warnings, fmt.Sprintf(
			"Tu cours environ %s par semaine en ce moment : c'est peu pour préparer %s, le volume devra monter progressivement.", kmFR(f.WeeklyKm), raceName))
	}
	if ref != nil && !stale && f.RecentRuns > 0 && f.RecentRuns < 3 {
		f.Warnings = append(f.Warnings, fmt.Sprintf("Seulement %d sortie(s) sur les trois derniers mois : l'estimation repose sur peu de données.", f.RecentRuns))
	}
	if ref != nil && f.VmaKmh == 0 && f.Reliability != ReliabilityGood {
		f.Warnings = append(f.Warnings, "Un test de VMA dans l'app (6 minutes) mesurerait ta vitesse réelle et resserrerait cette estimation.")
	}

	if f.TargetSec > 0 && f.PredictedSec > 0 {
		f.GapPct = (f.PredictedSec - f.TargetSec) / f.PredictedSec
	}
	switch {
	case trail:
		// Verdict laissé au coach, voir plus haut.
	case f.TargetSec > 0 && f.PredictedSec > 0:
		switch {
		case f.GapPct <= -0.10:
			f.Verdict = VerdictEasy
		case f.GapPct <= f.AllowedPct*0.5:
			f.Verdict = VerdictRealistic
		case f.GapPct <= f.AllowedPct:
			f.Verdict = VerdictStretch
		default:
			f.Verdict = VerdictTooHard
		}
		// Une course bien plus longue que tout ce qui a été couru reste un vrai
		// saut d'endurance, même à allure tranquille.
		if goalKm >= 21 && f.LongestRunKm < goalKm*0.4 && f.Verdict != VerdictTooHard {
			if f.Verdict == VerdictEasy || f.Verdict == VerdictRealistic {
				f.Verdict = VerdictStretch
			}
		}
		// Sur une estimation fragile, on n'annonce pas un objectif « largement à portée ».
		if f.Reliability == ReliabilityLow && f.Verdict == VerdictEasy {
			f.Verdict = VerdictRealistic
		}
	case f.TargetSec > 0 && f.TargetPaceSec >= 450:
		// Sans historique, une allure plus lente que 7:30/km est accessible en
		// alternant course et marche si besoin.
		f.Verdict = VerdictRealistic
	}
	return f
}

// approxDuration arrondit un temps à la précision que mérite l'estimation : à la
// seconde près, « 4h11'42 » laisse croire à une mesure. Une estimation fragile se
// dit à cinq minutes près sur une longue course, à la minute sur une courte.
func approxDuration(sec float64, reliability string) string {
	step := 60.0
	switch {
	case sec >= 3600 && reliability == ReliabilityLow:
		step = 300
	case sec < 3600 && reliability == ReliabilityGood:
		step = 10
	case sec < 3600 && reliability == ReliabilityMedium:
		step = 30
	}
	return formatDuration(math.Round(sec/step) * step)
}

func formatDuration(sec float64) string {
	s := int(math.Round(sec))
	h, m, r := s/3600, (s%3600)/60, s%60
	switch {
	case h > 0 && r == 0:
		return fmt.Sprintf("%dh%02d", h, m)
	case h > 0:
		return fmt.Sprintf("%dh%02d'%02d", h, m, r)
	case r == 0:
		return fmt.Sprintf("%d min", m)
	default:
		return fmt.Sprintf("%d min %02d s", m, r)
	}
}

// kmFR : « 10,1 km » — virgule décimale, comme dans le reste de l'app.
func kmFR(km float64) string {
	return strings.Replace(strconv.FormatFloat(km, 'f', 1, 64), ".", ",", 1) + " km"
}

// factsForPrompt : les chiffres que le coach doit reprendre tels quels.
func factsForPrompt(f FeasibilityFacts) string {
	var b strings.Builder
	b.WriteString("FAITS CALCULÉS (exacts, à reprendre tels quels) :\n")
	if f.TargetSec > 0 {
		fmt.Fprintf(&b, "- Chrono visé : %s, soit une allure de %s.\n", formatDuration(f.TargetSec), formatPace(f.TargetPaceSec))
	} else {
		b.WriteString("- Pas de chrono chiffré : l'objectif est de finir.\n")
	}
	if f.PredictedSec > 0 {
		if f.FastLabel == f.SlowLabel {
			fmt.Fprintf(&b, "- Niveau actuel estimé sur %s : environ %s (%s).\n",
				kmFR(f.GoalKm), f.FastLabel, formatPace(f.PredictedPaceSec))
		} else {
			fmt.Fprintf(&b, "- Niveau actuel estimé sur %s : entre %s et %s. Donne toujours cette fourchette, jamais un chrono unique ni un chiffre à la seconde.\n",
				kmFR(f.GoalKm), f.FastLabel, f.SlowLabel)
			fmt.Fprintf(&b, "  Le bas de la fourchette (%s) suppose une endurance déjà au point sur la distance ; le haut (%s) est ce que les sorties permettent de tenir pour acquis.\n",
				f.FastLabel, f.SlowLabel)
		}
		fmt.Fprintf(&b, "- Fiabilité de cette estimation : %s — %s.\n", f.Reliability, f.ReliabilityWhy)
	}
	if f.Reference != nil {
		fmt.Fprintf(&b, "- Sortie la plus probante : %s en %s (%s), le %s.\n",
			kmFR(f.Reference.DistanceKm), formatDuration(f.Reference.TimeSec),
			formatPace(f.Reference.TimeSec/f.Reference.DistanceKm), frDay(f.Reference.Date))
	}
	if f.VmaKmh > 0 {
		fmt.Fprintf(&b, "- VMA mesurée au test de l'app : %s km/h.\n", strings.Replace(strconv.FormatFloat(f.VmaKmh, 'f', 1, 64), ".", ",", 1))
	}
	if f.RecentRuns > 0 {
		fmt.Fprintf(&b, "- Sorties des trois derniers mois : %d.\n", f.RecentRuns)
	}
	if f.WeeklyKm > 0 {
		fmt.Fprintf(&b, "- Volume des quatre dernières semaines : environ %s par semaine.\n", kmFR(f.WeeklyKm))
	}
	if f.Trail {
		b.WriteString("- Course en trail : le dénivelé ralentit l'allure, la comparaison avec des sorties sur route est indicative.\n")
	}
	if f.LongestRunKm > 0 {
		fmt.Fprintf(&b, "- Plus longue sortie récente : %s.\n", kmFR(f.LongestRunKm))
	}
	if f.TargetSec > 0 && f.PredictedSec > 0 {
		switch {
		case f.TargetSec >= f.SlowSec:
			b.WriteString("- Le chrono visé est plus LENT que tout le niveau estimé : il ne demande aucune progression en vitesse.\n")
		case f.TargetSec >= f.FastSec:
			b.WriteString("- Le chrono visé tombe DANS la fourchette du niveau actuel : il est atteignable si l'endurance suit, ce n'est pas acquis.\n")
		default:
			fmt.Fprintf(&b, "- Le chrono visé est plus rapide que tout le niveau estimé : il demande de gagner au moins %.0f %% ; sur cette préparation, une progression d'environ %.0f %% est raisonnable.\n",
				(f.FastSec-f.TargetSec)/f.FastSec*100, f.AllowedPct*100)
		}
	}
	if len(f.Warnings) > 0 {
		b.WriteString("POINTS D'ATTENTION (à dire, avec leurs chiffres) :\n")
		for _, w := range f.Warnings {
			b.WriteString("- " + w + "\n")
		}
	}
	return b.String()
}

// verdictInstruction : le verdict est imposé quand il a pu être calculé.
func verdictInstruction(f FeasibilityFacts) string {
	if f.Verdict != "" {
		return "Écris exactement ce verdict, sans le changer : **" + f.Verdict + "**"
	}
	return "Une seule ligne parmi : **" + VerdictEasy + "** ou **" + VerdictRealistic + "** ou **" + VerdictStretch + "** ou **" + VerdictTooHard + "**"
}

// runHistory rassemble ce que la personne a couru : ses sorties Strava et ses
// courses NeuroRun (montre ou téléphone), sans compter deux fois une course
// publiée sur Strava. Les courses NeuroRun prennent la même forme que le résumé
// Strava, pour que le calcul et le coach lisent une seule liste.
func (h *Handlers) runHistory(ctx context.Context, u *models.User, stravaActs []map[string]any, now time.Time) []map[string]any {
	acts := append([]map[string]any(nil), stravaActs...)
	var stravaStarts []time.Time
	for _, a := range stravaActs {
		if d, _ := a["start_date"].(string); d != "" {
			if t, err := time.Parse(time.RFC3339, d); err == nil {
				stravaStarts = append(stravaStarts, t)
			}
		}
	}
	lives, err := h.db.ListLiveRunsSince(ctx, u.ID, now.AddDate(0, 0, -2*feasibilityRecentDays))
	if err != nil {
		return acts
	}
	for _, lr := range lives {
		if lr.DistanceM < 500 || lr.MovingSec < 120 {
			continue
		}
		start := goalcalendar.LiveRunStart(lr)
		dup := false
		for _, s := range stravaStarts {
			if d := s.Sub(start); d > -15*time.Minute && d < 15*time.Minute {
				dup = true
				break
			}
		}
		if dup {
			continue
		}
		name := strings.TrimSpace(lr.Name)
		if name == "" {
			name = "Course NeuroRun"
		}
		acts = append(acts, map[string]any{
			"name":            name,
			"type":            "Run",
			"start_date":      start.UTC().Format(time.RFC3339),
			"distance_km":     math.Round(lr.DistanceM/10) / 100,
			"moving_time_min": math.Round(lr.MovingSec/6) / 10,
			"source":          "neurorun",
		})
	}
	return acts
}

// latestVmaReference : le dernier test de VMA, nil s'il n'y en a pas.
func (h *Handlers) latestVmaReference(ctx context.Context, u *models.User) *VmaReference {
	t, err := h.db.LatestVmaTest(ctx, u.ID)
	if err != nil || t == nil || !vma.PlausibleVma(t.VmaKmh) {
		return nil
	}
	return &VmaReference{Kmh: t.VmaKmh, At: t.CreatedAt, Km: t.DistanceM / 1000}
}

// GoalFeasibility POST /api/goals/feasibility — l'avis du coach sur un objectif
// avant de créer le plan. Le niveau, sa fourchette, sa fiabilité et le verdict
// sont calculés ici ; le modèle les explique, il ne les invente pas.
func (h *Handlers) GoalFeasibility(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser{}).(*models.User)
	if !h.requireCapability(w, r, u, "goals") {
		return
	}

	b, label, distKm, targetTime, errHTTP, errMsg := validateGoalPayload(r)
	if errHTTP != 0 {
		writeJSON(w, errHTTP, map[string]string{"error": errMsg})
		return
	}

	var stravaActs []map[string]any
	if u.HasStrava() {
		access, err := h.ensureStravaAccess(r.Context(), u)
		if err != nil {
			h.writeStravaError(w, r, u, err, "impossible d'accéder à Strava, reconnectez le compte")
			return
		}
		stravaActs, err = h.strava.ActivitiesSummary(r.Context(), access, 50)
		if err != nil {
			h.writeStravaError(w, r, u, err, "erreur Strava")
			return
		}
	}
	now := time.Now().UTC()
	acts := h.runHistory(r.Context(), u, stravaActs, now)
	test := h.latestVmaReference(r.Context(), u)
	// Le verdict se calcule sur les sorties ; le coach ne fait que l'expliquer.
	facts := assessFeasibilityAt(acts, test, distKm, targetTime, b.Weeks, b.SessionsPerWeek, b.Terrain == models.GoalTerrainTrail, now)

	system, userQ := feasibilityPrompts(label, b.Terrain, targetTime, b.Weeks, b.SessionsPerWeek, facts, acts)
	text, err := h.ai.Chat(r.Context(), system, userQ)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "erreur IA"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"feasibility": text, "facts": facts})
}

// feasibilityPrompts : la consigne et la question envoyées au modèle. Tout ce qui
// est chiffré vient des faits calculés ; le modèle rédige, il ne juge pas.
func feasibilityPrompts(label, terrain, targetTime string, weeks, spw int, facts FeasibilityFacts, acts []map[string]any) (string, string) {
	userQ := `Objectif course : ` + label + `.
Type de course : ` + models.GoalTerrainLabel(terrain) + `.
Chrono ou intention : ` + targetTime + `.
Échéance dans ` + strconv.Itoa(weeks) + ` semaine(s).
Disponibilité : ` + strconv.Itoa(spw) + ` séance(s) par semaine en moyenne.

` + factsForPrompt(facts) + `
Donne uniquement le verdict et la justification demandés.`

	rules := `Règles de lecture, à ne jamais inverser :
- Un chrono visé PLUS LENT que le niveau actuel est FACILE côté vitesse : il ne demande aucune progression. Ne le présente jamais comme irréaliste.
- Un chrono visé plus rapide que le niveau actuel demande de progresser : compare l'écart à la progression raisonnable indiquée dans les faits.
- Reprends les chiffres des FAITS CALCULÉS tels quels ; n'en invente aucun autre, et ne les rends pas plus précis qu'ils ne sont (pas de secondes sur une fourchette donnée à la minute ou à cinq minutes).
- Quand le niveau est donné en fourchette, donne la fourchette : ne choisis pas un chrono au milieu.
- Ne dis jamais qu'une sortie « prouve » un niveau sur une distance bien plus longue qu'elle : elle l'indique, pas plus.
- Dis la fiabilité telle qu'elle est écrite dans les faits, sans l'adoucir ni la dramatiser.
- Reprends chaque POINT D'ATTENTION, avec ses chiffres.
- Écris les nombres à la française : virgule décimale (« 10,1 km »), allures en min:s/km (« 5:24/km »).`

	format := `Réponds UNIQUEMENT avec ce format Markdown (rien d'autre — pas de plan d'entraînement) :

## Verdict
` + verdictInstruction(facts) + `

## En une phrase
Une phrase simple qui résume pourquoi, en citant le niveau actuel (sa fourchette) et le chrono visé.

## Ce que disent tes données
3 à 4 puces maximum, appuyées sur les faits calculés (niveau, sortie la plus probante, VMA mesurée, volume, régularité).

## Fiabilité de cet avis
Une ou deux phrases : le niveau de fiabilité écrit dans les faits, pourquoi, et ce qui le rendrait plus sûr.

## Points d'attention
Une puce par point d'attention des faits. Supprime cette section s'il n'y en a aucun.

## Pour mettre toutes les chances de ton côté
2 ou 3 puces concrètes : ce qu'il faut ajuster ou soigner (chrono, durée, nombre de séances, sorties longues, test de VMA).`

	var system string
	if facts.PredictedSec > 0 {
		actsJSON, _ := json.Marshal(acts)
		system = `Tu es un coach course à pied. Tu écris en français, en TUTOIEMENT, phrases courtes (une idée par phrase). Niveau débutant : évite le jargon ou explique en une parenthèse (ex. « allure = minutes pour faire 1 km »).

Tu reçois un objectif, des FAITS CALCULÉS sur les sorties de la personne (Strava et courses enregistrées dans l'app NeuroRun) et la liste de ces sorties en JSON pour le contexte (fréquence, régularité, distances).

` + rules + `

Ton ton est bienveillant, honnête et **inclusif** : pas de stéréotypes de genre ou de « niveau normal ». Tu ne vends pas du rêve : un avis prudent mais juste vaut mieux qu'un avis flatteur.

` + format + `

**Sorties (JSON) :** ` + string(actsJSON)
	} else {
		system = `Tu es un coach course à pied. Tu écris en français, en TUTOIEMENT, phrases courtes. Niveau débutant : évite le jargon ou explique en une parenthèse (ex. « allure = minutes pour faire 1 km »).

Tu n’as **aucune sortie exploitable** de la personne : tu te bases sur l’objectif déclaré et les FAITS CALCULÉS (allure visée). Dis-le clairement : sans sortie, tu ne peux pas juger son niveau, seulement ce que demande l'objectif.

` + rules + `

Ton ton est bienveillant, honnête et **inclusif**.

Réponds UNIQUEMENT avec ce format Markdown (rien d'autre — pas de plan d'entraînement) :

## Verdict
` + verdictInstruction(facts) + `

## En une phrase
Une phrase simple qui résume pourquoi.

## Ce que demande cet objectif
3 puces : l’allure visée, le délai et le nombre de séances — sans prétendre connaître l’entraînement réel de la personne.

## Fiabilité de cet avis
Une phrase : avis à prendre avec prudence, faute de sorties. Enregistrer deux ou trois courses avec l'app, passer le test de VMA ou lier Strava permet un avis chiffré.

## Pour mettre toutes les chances de ton côté
2 ou 3 puces : ajuster le chrono, le délai ou le nombre de séances.`
	}

	return system, userQ
}

// frDay : « 5 novembre 2026 » pour un jour AAAA-MM-JJ ; la chaîne telle quelle sinon.
func frDay(iso string) string {
	t, err := time.Parse("2006-01-02", iso)
	if err != nil {
		return iso
	}
	return fmt.Sprintf("%d %s %d", t.Day(), frMonths[t.Month()-1], t.Year())
}
