package goalcalendar

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"

	"runapp/internal/models"
)

var (
	reSessionKm      = regexp.MustCompile(`(?i)(\d+(?:[.,]\d+)?)\s*km\b`)
	reSessionReps    = regexp.MustCompile(`(?i)(\d+)\s*[x×]\s*(\d+(?:[ .,]\d+)*)\s*(km|m)\b`)
	reSessionRecover = regexp.MustCompile(`(?i)r[ée]cup[^;,.\n]*`)
	reSessionPace    = regexp.MustCompile(`(?i)(\d+):(\d{2})(?:\s*[–-]\s*\d+:\d{2})?\s*(?:min)?\s*/\s*km`)
	reSessionClock   = regexp.MustCompile(`(?i)\d+:\d{2}(?:\s*(?:min|/)\s*\S*)?`)
	reSessionRaceRef = regexp.MustCompile(`(?i)allure\s+\d+\s*km`)
	reSessionHours   = regexp.MustCompile(`(?i)\b(\d+)\s*h(\d{2})?\b`)
	reSessionMinutes = regexp.MustCompile(`(?i)(\d+)\s*min\b`)
)

// Allure de conversion d'une séance donnée en durée quand le texte n'en cite aucune.
const fallbackPaceSecPerKm = 360

// fallbackSummaryRunes : au-delà, le résumé déborde de sa ligne dans le calendrier.
const fallbackSummaryRunes = 110

func parseDecimal(s string) float64 {
	v, err := strconv.ParseFloat(strings.ReplaceAll(s, ",", "."), 64)
	if err != nil || v <= 0 {
		return 0
	}
	return v
}

// estimateSessionKm additionne ce que la séance fait courir : répétitions, distances
// (échauffement et retour au calme compris) et durées converties à l'allure citée.
// Les récupérations entre répétitions ne comptent pas : la distance reste un minimum.
func estimateSessionKm(text string) float64 {
	paceSec := float64(fallbackPaceSecPerKm)
	if m := reSessionPace.FindStringSubmatch(text); m != nil {
		min, _ := strconv.Atoi(m[1])
		sec, _ := strconv.Atoi(m[2])
		if p := min*60 + sec; p > 0 {
			paceSec = float64(p)
		}
	}

	s := reSessionRaceRef.ReplaceAllString(text, "")
	km := 0.0
	reps := reSessionReps.FindAllStringSubmatch(s, -1)
	for _, m := range reps {
		n, _ := strconv.Atoi(m[1])
		dist := parseDecimal(strings.ReplaceAll(m[2], " ", ""))
		if strings.EqualFold(m[3], "m") {
			dist /= 1000
		}
		km += float64(n) * dist
	}
	if len(reps) > 0 {
		s = reSessionReps.ReplaceAllString(s, "")
		s = reSessionRecover.ReplaceAllString(s, "")
	}
	s = reSessionClock.ReplaceAllString(s, "")

	for _, m := range reSessionKm.FindAllStringSubmatch(s, -1) {
		km += parseDecimal(m[1])
	}
	seconds := 0
	for _, m := range reSessionHours.FindAllStringSubmatch(s, -1) {
		h, _ := strconv.Atoi(m[1])
		min, _ := strconv.Atoi(m[2])
		seconds += h*3600 + min*60
	}
	for _, m := range reSessionMinutes.FindAllStringSubmatch(s, -1) {
		min, _ := strconv.Atoi(m[1])
		seconds += min * 60
	}
	km += float64(seconds) / paceSec
	return math.Round(km*10) / 10
}

func sessionSummary(text string) string {
	r := []rune(text)
	if len(r) <= fallbackSummaryRunes {
		return text
	}
	return strings.TrimSpace(string(r[:fallbackSummaryRunes-1])) + "…"
}

// sessionAt : le numéro écrit fait foi, la position sert de repli quand le plan ne
// numérote pas ses séances dans l'ordre.
func sessionAt(sessions []planSession, s int) (planSession, bool) {
	for _, it := range sessions {
		if it.number == s {
			return it, true
		}
	}
	if s-1 < len(sessions) {
		return sessions[s-1], true
	}
	return planSession{}, false
}

// FallbackPlannedSessionsFromPlan relit les séances dans le Markdown quand l'extraction
// par le modèle a échoué. Une séance reste à sa place même si sa distance ne se lit pas :
// elle reprend alors la précédente, sans décaler les suivantes.
func FallbackPlannedSessionsFromPlan(plan string, weeks, spw int) []models.PlannedSession {
	if weeks < 1 || spw < 1 || plan == "" {
		return nil
	}
	blocks := planWeekBlocks(plan)

	var out []models.PlannedSession
	last := 6.0
	for w := 1; w <= weeks; w++ {
		sessions := weekSessions(blocks[w])
		for s := 1; s <= spw; s++ {
			summary := ""
			dk := 0.0
			if it, ok := sessionAt(sessions, s); ok {
				summary = sessionSummary(it.text)
				dk = estimateSessionKm(it.text)
			}
			if dk < 0.5 {
				dk = last
			}
			last = dk
			if summary == "" {
				summary = fmt.Sprintf("Semaine %d · séance %d (~%.1f km)", w, s, dk)
			}
			out = append(out, models.PlannedSession{
				Week:       w,
				Session:    s,
				DistanceKm: dk,
				Summary:    summary,
			})
		}
	}
	return out
}
