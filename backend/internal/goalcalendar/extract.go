package goalcalendar

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"runapp/internal/models"
)

// ChatExtractor appelle le modèle (extrait JSON).
type ChatExtractor interface {
	Chat(ctx context.Context, systemPrompt, userMessage string) (string, error)
}

type rawPlanned struct {
	Week           int      `json:"week"`
	Session        int      `json:"session"`
	DistanceKm     float64  `json:"distance_km"`
	PaceSecPerKm   *float64 `json:"pace_sec_per_km"`
	Summary        string   `json:"summary"`
}

// ExtractPlannedSessions demande à l’IA une liste JSON strictement typée.
func ExtractPlannedSessions(ctx context.Context, cx ChatExtractor, plan string, weeks, spw int) ([]models.PlannedSession, error) {
	if weeks < 1 || spw < 1 || cx == nil || strings.TrimSpace(plan) == "" {
		return nil, errors.New("bad args")
	}
	n := weeks * spw
	system := `Tu es un assistant technique course à pied. Tu extrais les séances d’un plan Markdown.

Réponds avec UN SEUL tableau JSON valide (pas de markdown, pas de texte avant ou après).

Chaque élément du tableau a exactement les clés :
- "week" : entier 1..W
- "session" : entier 1..S (ordre Séance 1, 2… dans la semaine)
- "distance_km" : nombre décimal = distance totale **minimum** à couvrir sur Strava ce jour (échauff + corps + retour). Pour une sortie « 6 km facile », mets 6.
- "pace_sec_per_km" : nombre (secondes pour parcourir 1 km en allure moyenne **cible pour toute la sortie**) ou null si le plan ne permet pas de l’estimer sans ambiguïté.
- "summary" : courte phrase (facultatif, peut être "").

Le tableau doit contenir **exactement** W×S entrées, dans l’ordre : semaine 1 sessions 1..S, semaine 2… jusqu’à la semaine W.`

	user := strings.Builder{}
	user.WriteString("W=")
	user.WriteString(strconv.Itoa(weeks))
	user.WriteString(" S=")
	user.WriteString(strconv.Itoa(spw))
	user.WriteString(" → exactement ")
	user.WriteString(strconv.Itoa(n))
	user.WriteString(" objets JSON.\n\n--- PLAN ---\n")
	user.WriteString(plan)

	raw, err := cx.Chat(ctx, system, user.String())
	if err != nil {
		return nil, err
	}
	raw = strings.TrimSpace(raw)
	raw = stripJSONFence(raw)
	i := strings.Index(raw, "[")
	j := strings.LastIndex(raw, "]")
	if i < 0 || j <= i {
		return nil, errors.New("no json array")
	}
	raw = raw[i : j+1]

	var rows []rawPlanned
	if err := json.Unmarshal([]byte(raw), &rows); err != nil {
		return nil, err
	}
	if len(rows) != n {
		return nil, errors.New("wrong count")
	}

	out := make([]models.PlannedSession, 0, len(rows))
	for _, r := range rows {
		if r.Week < 1 || r.Week > weeks || r.Session < 1 || r.Session > spw {
			return nil, errors.New("invalid week/session")
		}
		if r.DistanceKm < 0.2 {
			return nil, errors.New("invalid distance")
		}
		p := r.PaceSecPerKm
		if p != nil && (*p < 120 || *p > 1200) {
			p = nil
		}
		out = append(out, models.PlannedSession{
			Week:           r.Week,
			Session:        r.Session,
			DistanceKm:     r.DistanceKm,
			PaceSecPerKm:   p,
			Summary:        strings.TrimSpace(r.Summary),
		})
	}
	return out, nil
}

func stripJSONFence(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		s = strings.TrimPrefix(s, "```")
		s = strings.TrimPrefix(s, "json")
		s = strings.TrimSpace(s)
		if idx := strings.Index(s, "```"); idx >= 0 {
			s = s[:idx]
		}
	}
	return strings.TrimSpace(s)
}
