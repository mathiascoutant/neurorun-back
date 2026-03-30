package handlers

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strings"

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
			if legCopy.DistanceKm > 0 {
				legCopy.PaceSecPerKm = math.Round(legCopy.TimeSec / legCopy.DistanceKm)
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
	if !u.HasStrava() {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "connectez Strava d'abord"})
		return
	}
	access, err := h.ensureStravaAccess(r.Context(), u)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "impossible d'accéder à Strava, reconnectez le compte"})
		return
	}
	runs, err := h.strava.FetchRunActivities(r.Context(), access, nil)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "erreur Strava"})
		return
	}
	payload := strava.BuildRaceForecast(runs)
	writeJSON(w, http.StatusOK, payload)
}

func (h *Handlers) StravaRaceForecastAdjust(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST uniquement"})
		return
	}
	u := r.Context().Value(ctxUser{}).(*models.User)
	if !u.HasStrava() {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "connectez Strava d'abord"})
		return
	}
	var b forecastAdjustBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}

	access, err := h.ensureStravaAccess(r.Context(), u)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "impossible d'accéder à Strava, reconnectez le compte"})
		return
	}
	runs, err := h.strava.FetchRunActivities(r.Context(), access, nil)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "erreur Strava"})
		return
	}
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
