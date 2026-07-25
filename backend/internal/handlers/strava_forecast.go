package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"

	"runapp/internal/models"
	"runapp/internal/strava"
)

type forecastAdjustBody struct {
	Energy  string `json:"energy"` // great | normal | tired
	Injured bool   `json:"injured"`
}

type forecastFactorsJSON struct {
	F5        float64 `json:"5k"`
	F10       float64 `json:"10k"`
	FHalf     float64 `json:"half"`
	FMar      float64 `json:"marathon"`
	Rationale string  `json:"rationale_fr"`
}

func clampFactor(f float64) float64 {
	if f < 0.88 {
		return 0.88
	}
	if f > 1.22 {
		return 1.22
	}
	return f
}

func heuristicFactors(energy string, injured bool) forecastFactorsJSON {
	e := strings.ToLower(strings.TrimSpace(energy))
	base := 1.0
	switch e {
	case "great", "excellent", "top":
		base = 0.98
	case "tired", "fatigue", "fatigué", "fatiguee":
		base = 1.06
	case "normal", "ok", "":
		base = 1.0
	default:
		base = 1.0
	}
	inj := 1.0
	if injured {
		inj = 1.12
	}
	combo := base * inj
	return forecastFactorsJSON{
		F5:        combo,
		F10:       combo,
		FHalf:     combo * 1.01,
		FMar:      combo * 1.02,
		Rationale: "Ajustement automatique : facteur dérivé du ressenti et de l’état blessure (secours si l’IA est indisponible).",
	}
}

func parseFactorsFromAI(raw string) (forecastFactorsJSON, error) {
	s := strings.TrimSpace(raw)
	if i := strings.Index(s, "{"); i >= 0 {
		s = s[i:]
	}
	if j := strings.LastIndex(s, "}"); j >= 0 {
		s = s[:j+1]
	}
	var f forecastFactorsJSON
	if err := json.Unmarshal([]byte(s), &f); err != nil {
		return f, err
	}
	if f.F5 <= 0 {
		f.F5 = 1
	}
	if f.F10 <= 0 {
		f.F10 = 1
	}
	if f.FHalf <= 0 {
		f.FHalf = 1
	}
	if f.FMar <= 0 {
		f.FMar = 1
	}
	return f, nil
}

func applyForecastFactors(base strava.RaceForecastPayload, f forecastFactorsJSON) strava.RaceForecastPayload {
	f5 := clampFactor(f.F5)
	f10 := clampFactor(f.F10)
	fh := clampFactor(f.FHalf)
	fm := clampFactor(f.FMar)
	factors := []float64{f5, f10, fh, fm}
	out := base
	out.Legs = make([]strava.RaceLegForecast, len(base.Legs))
	for i, leg := range base.Legs {
		if i >= len(factors) {
			out.Legs[i] = leg
			continue
		}
		fac := factors[i]
		legCopy := leg
		if legCopy.TimeSec > 0 {
			b := legCopy.TimeSec
			legCopy.BaselineTimeSec = &b
			legCopy.TimeSec = math.Round(legCopy.TimeSec * fac)
			// Distance officielle précise (21,0975 / 42,195…), pas l'arrondi d'affichage.
			d := strava.StandardDistanceKm(legCopy.ID)
			if d <= 0 {
				d = legCopy.DistanceKm
			}
			if d > 0 {
				legCopy.PaceSecPerKm = math.Round(legCopy.TimeSec / d)
			}
		}
		out.Legs[i] = legCopy
	}
	return out
}

func (h *Handlers) StravaRaceForecast(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET uniquement"})
		return
	}
	u := r.Context().Value(ctxUser{}).(*models.User)
	if !h.requireCapability(w, r, u, "forecast") {
		return
	}
	runs := h.gatherForecastRuns(r.Context(), u)
	payload := strava.BuildRaceForecast(runs)
	writeJSON(w, http.StatusOK, payload)
}

func (h *Handlers) StravaRaceForecastAdjust(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST uniquement"})
		return
	}
	u := r.Context().Value(ctxUser{}).(*models.User)
	if !h.requireCapability(w, r, u, "forecast") {
		return
	}
	var b forecastAdjustBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}

	runs := h.gatherForecastRuns(r.Context(), u)
	base := strava.BuildRaceForecast(runs)

	aiKey := strings.TrimSpace(h.cfg.OpenAIAPIKey) != ""
	var fac forecastFactorsJSON
	var aiErr error
	aiUsed := false

	if aiKey {
		var legsSummary strings.Builder
		for _, leg := range base.Legs {
			legsSummary.WriteString(leg.ID)
			legsSummary.WriteString(": ")
			legsSummary.WriteString(formatSecForPrompt(leg.TimeSec))
			legsSummary.WriteString(" ; ")
		}
		prompt := `Tu es un coach course à pied. L'utilisateur a des prévisions de temps brutes (stats Strava).
Ressenti (energy): "` + strings.TrimSpace(b.Energy) + `"
Blessure actuelle (injured): ` + boolStr(b.Injured) + `

Prévisions de base (temps total approximatif par épreuve): ` + legsSummary.String() + `

Réponds UNIQUEMENT par un objet JSON valide, sans markdown, avec les clés exactes:
{"5k": number, "10k": number, "half": number, "marathon": number, "rationale_fr": string}

Chaque number est un facteur multiplicateur sur le temps prévu (1.0 = inchangé, 1.08 = environ 8% plus lent, 0.98 = un peu plus rapide).
Adapte légèrement par distance si pertinent (ex. blessure : impact plus marqué sur le marathon).
Reste réaliste : facteurs entre 0.9 et 1.18. rationale_fr : 1 à 2 phrases en français.`

		raw, err := h.openai.Chat(r.Context(), "Tu réponds uniquement en JSON minimal, sans balises.", prompt)
		if err != nil {
			aiErr = err
		} else {
			fac, err = parseFactorsFromAI(raw)
			if err != nil {
				aiErr = err
			} else {
				aiUsed = true
			}
		}
	}

	if !aiUsed {
		fac = heuristicFactors(b.Energy, b.Injured)
		if aiErr != nil {
			fac.Rationale = fac.Rationale + " (L’IA n’a pas répondu — secours appliqué.)"
		}
	}

	adjusted := applyForecastFactors(base, fac)
	writeJSON(w, http.StatusOK, map[string]any{
		"baseline":     base,
		"adjusted":     adjusted,
		"rationale_fr": fac.Rationale,
		"ai_used":      aiUsed,
		"factors":      fac,
	})
}

// gatherForecastRuns rassemble toutes les sorties exploitables pour la prévision :
// activités Strava (si le compte est lié) + courses NeuroRun (montre/live), ces dernières
// dédupliquées contre Strava. La prévision fonctionne donc même sans Strava.
func (h *Handlers) gatherForecastRuns(ctx context.Context, u *models.User) []strava.RunActivity {
	var runs []strava.RunActivity

	if u.HasStrava() {
		if access, err := h.ensureStravaAccess(ctx, u); err == nil {
			if acts, err := h.strava.FetchRunActivities(ctx, access, nil); err == nil {
				runs = append(runs, acts...)
			}
		}
	}

	if live, err := h.db.ListLiveRunsByUser(ctx, u.ID, 300); err == nil {
		for i := range live {
			ra, ok := liveRunToActivity(&live[i])
			if !ok {
				continue
			}
			if isDuplicateOfStrava(ra, runs) {
				continue
			}
			runs = append(runs, ra)
		}
	}
	return runs
}

// liveRunToActivity convertit une course NeuroRun en RunActivity (allure = temps de MOUVEMENT).
func liveRunToActivity(lr *models.LiveRun) (strava.RunActivity, bool) {
	if lr.DistanceM < 100 || lr.MovingSec <= 0 {
		return strava.RunActivity{}, false
	}
	start := lr.CreatedAt
	if lr.GpsStartTsMs > 0 {
		start = time.UnixMilli(lr.GpsStartTsMs)
	}
	ra := strava.RunActivity{
		Name:       "NeuroRun",
		Type:       "Run",
		StartAt:    start.UTC(),
		DistanceM:  lr.DistanceM,
		MovingSec:  int(math.Round(lr.MovingSec)),
		ElapsedSec: int(math.Round(lr.WallSec)),
		AvgSpeed:   lr.DistanceM / lr.MovingSec, // m/s
	}
	if hr := avgHRFromTrack(lr.TrackPoints); hr > 0 {
		ra.AvgHR = &hr
	}
	return ra, true
}

func avgHRFromTrack(pts []models.LiveRunTrackPoint) float64 {
	var sum float64
	var n int
	for _, p := range pts {
		if p.HrBpm != nil && *p.HrBpm > 0 {
			sum += *p.HrBpm
			n++
		}
	}
	if n == 0 {
		return 0
	}
	return sum / float64(n)
}

// isDuplicateOfStrava évite de compter deux fois une sortie présente à la fois côté montre
// et côté Strava (départ à < 4 min et distance à < 10 %). Ne compare qu'aux activités Strava.
func isDuplicateOfStrava(live strava.RunActivity, existing []strava.RunActivity) bool {
	if live.DistanceM <= 0 {
		return false
	}
	for _, e := range existing {
		if e.ID == 0 { // 0 = course NeuroRun, pas une activité Strava
			continue
		}
		dt := e.StartAt.Sub(live.StartAt)
		if dt < 0 {
			dt = -dt
		}
		if dt > 4*time.Minute {
			continue
		}
		if math.Abs(e.DistanceM-live.DistanceM)/live.DistanceM < 0.1 {
			return true
		}
	}
	return false
}

func boolStr(v bool) string {
	if v {
		return "oui"
	}
	return "non"
}

func formatSecForPrompt(sec float64) string {
	if sec <= 0 {
		return "N/A"
	}
	s := int64(math.Round(sec))
	h := s / 3600
	m := (s % 3600) / 60
	rs := s % 60
	if h > 0 {
		return formatDur(h, m, rs)
	}
	return formatDur(0, m, rs)
}

func formatDur(h, m, rs int64) string {
	if h > 0 {
		return fmt.Sprintf("%dh %02d min %02d s", h, m, rs)
	}
	return fmt.Sprintf("%d min %02d s", m, rs)
}
