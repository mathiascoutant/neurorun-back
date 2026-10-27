package handlers

import (
	"fmt"
	"math"
	"regexp"
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

// FeasibilityFacts : ce que l'on sait avant d'écrire l'avis. Les champs à zéro
// sont inconnus (pas de chrono chiffré, pas de sortie exploitable).
type FeasibilityFacts struct {
	Verdict          string  `json:"verdict"`
	GoalKm           float64 `json:"goal_km"`
	TargetSec        float64 `json:"target_sec"`
	TargetPaceSec    float64 `json:"target_pace_sec_per_km"`
	PredictedSec     float64 `json:"predicted_sec"`
	PredictedPaceSec float64 `json:"predicted_pace_sec_per_km"`
	// GapPct : effort demandé par rapport au niveau actuel. Positif = le chrono
	// visé est plus rapide que ce que tu cours aujourd'hui.
	GapPct float64 `json:"gap_pct"`
	// AllowedPct : progression raisonnable sur la préparation choisie.
	AllowedPct   float64         `json:"allowed_pct"`
	Reference    *FeasibilityRun `json:"reference_run,omitempty"`
	LongestRunKm float64         `json:"longest_run_km"`
	Trail        bool            `json:"trail"`
	RunsAnalyzed int             `json:"runs_analyzed"`
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

// assessFeasibility calcule le niveau actuel sur les sorties et le compare au
// chrono visé. Le niveau est une borne basse : une sortie d'entraînement n'est
// pas une course à fond, donc la prédiction est prudente — un objectif plus
// lent qu'elle est d'autant plus sûr.
//
// En trail, le dénivelé fausse la comparaison avec des allures sur route : les
// chiffres sont fournis au coach, mais le verdict ne lui est pas imposé.
func assessFeasibility(acts []map[string]any, goalKm float64, targetTime string, weeks, sessionsPerWeek int, trail bool) FeasibilityFacts {
	goalKm = raceDistanceKm(goalKm)
	f := FeasibilityFacts{GoalKm: goalKm, AllowedPct: allowedProgress(weeks, sessionsPerWeek), Trail: trail}
	if sec, ok := parseTargetSeconds(targetTime); ok {
		f.TargetSec = sec
		f.TargetPaceSec = math.Round(sec / goalKm)
	}

	type candidate struct {
		vma float64
		run FeasibilityRun
	}
	var runs []candidate
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
		if km > f.LongestRunKm {
			f.LongestRunKm = km
		}
		implied := (km / (min / 60)) / vma.FractionOfVma(km)
		if !vma.PlausibleVma(implied) {
			continue
		}
		name, _ := a["name"].(string)
		date, _ := a["start_date"].(string)
		if t, err := time.Parse(time.RFC3339, date); err == nil {
			date = t.Format("2006-01-02")
		}
		runs = append(runs, candidate{implied, FeasibilityRun{Name: name, Date: date, DistanceKm: km, TimeSec: math.Round(min * 60)}})
	}

	// Plus une sortie est courte, plus l'extrapolation vers la distance visée
	// est optimiste : 1,5 km vite ne dit rien de l'endurance sur 5 km. Le niveau
	// se lit donc d'abord sur les sorties d'au moins 3 km et 40 % de l'objectif,
	// puis, faute de mieux, sur 3 km, et seulement en dernier recours plus court.
	bestVma := 0.0
	for _, minKm := range []float64{math.Max(3, goalKm*0.4), 3, 1.5} {
		for _, c := range runs {
			if c.run.DistanceKm >= minKm && c.vma > bestVma {
				bestVma = c.vma
				ref := c.run
				f.Reference = &ref
			}
		}
		if bestVma > 0 {
			break
		}
	}
	if bestVma > 0 {
		t := vma.TargetFor(bestVma, goalKm)
		f.PredictedSec = t.TimeSec
		f.PredictedPaceSec = t.PaceSecPerKm
	}

	if f.TargetSec > 0 && f.PredictedSec > 0 {
		f.GapPct = (f.PredictedSec - f.TargetSec) / f.PredictedSec
	}
	switch {
	case trail:
		// Verdict laissé au coach, voir plus haut.
	case f.TargetSec > 0 && f.PredictedSec > 0:
		f.GapPct = (f.PredictedSec - f.TargetSec) / f.PredictedSec
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
	case f.TargetSec > 0 && f.TargetPaceSec >= 450:
		// Sans historique, une allure plus lente que 7:30/km est accessible en
		// alternant course et marche si besoin.
		f.Verdict = VerdictRealistic
	}
	return f
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
	if f.Reference != nil {
		fmt.Fprintf(&b, "- Sortie de référence : %s en %s (%s), le %s.\n",
			kmFR(f.Reference.DistanceKm), formatDuration(f.Reference.TimeSec),
			formatPace(f.Reference.TimeSec/f.Reference.DistanceKm), f.Reference.Date)
		fmt.Fprintf(&b, "- Niveau actuel estimé sur %s : %s (%s). C'est une estimation prudente, faite sur des sorties d'entraînement.\n",
			kmFR(f.GoalKm), formatDuration(f.PredictedSec), formatPace(f.PredictedPaceSec))
	}
	if f.Trail {
		b.WriteString("- Course en trail : le dénivelé ralentit l'allure, la comparaison avec des sorties sur route est indicative.\n")
	}
	if f.LongestRunKm > 0 {
		fmt.Fprintf(&b, "- Plus longue sortie récente : %s.\n", kmFR(f.LongestRunKm))
	}
	if f.TargetSec > 0 && f.PredictedSec > 0 {
		if f.GapPct < 0 {
			fmt.Fprintf(&b, "- Le chrono visé est %.0f %% plus LENT que le niveau actuel : il ne demande aucune progression.\n", -f.GapPct*100)
		} else {
			fmt.Fprintf(&b, "- Le chrono visé demande de gagner %.0f %% ; sur cette préparation, une progression d'environ %.0f %% est raisonnable.\n", f.GapPct*100, f.AllowedPct*100)
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
