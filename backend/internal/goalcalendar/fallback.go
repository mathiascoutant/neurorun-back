package goalcalendar

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"runapp/internal/models"
)

var reWeekHeader = regexp.MustCompile(`(?i)###\s*Semaine\s*(\d+)`)
var reSeanceLine = regexp.MustCompile(`(?i)Séance\s*\d+\s*:\s*([^\n]+)`)
var reKm = regexp.MustCompile(`(\d+(?:[.,]\d+)?)\s*km`)

func parseKmOnLine(line string) float64 {
	m := reKm.FindStringSubmatch(line)
	if len(m) < 2 {
		return 0
	}
	s := strings.ReplaceAll(m[1], ",", ".")
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v <= 0 {
		return 0
	}
	return v
}

// FallbackPlannedSessionsFromPlan extrait semaine par semaine les volumes « km » du Markdown.
func FallbackPlannedSessionsFromPlan(plan string, weeks, spw int) []models.PlannedSession {
	if weeks < 1 || spw < 1 || plan == "" {
		return nil
	}
	// Découpe le plan en blocs après chaque "### Semaine N"
	idx := reWeekHeader.FindAllStringSubmatchIndex(plan, -1)
	byWeek := make(map[int][]float64)
	for i, loc := range idx {
		w, _ := strconv.Atoi(plan[loc[2]:loc[3]])
		start := loc[1]
		end := len(plan)
		if i+1 < len(idx) {
			end = idx[i+1][0]
		}
		block := plan[start:end]
		var dists []float64
		for _, sm := range reSeanceLine.FindAllStringSubmatch(block, -1) {
			if len(sm) < 2 {
				continue
			}
			if k := parseKmOnLine(sm[1]); k > 0 {
				dists = append(dists, k)
			}
		}
		byWeek[w] = dists
	}

	var out []models.PlannedSession
	last := 6.0
	for w := 1; w <= weeks; w++ {
		dists := byWeek[w]
		for len(dists) < spw {
			if len(dists) > 0 {
				last = dists[len(dists)-1]
			}
			dists = append(dists, last)
		}
		if len(dists) > spw {
			dists = dists[:spw]
		}
		for s := 1; s <= spw; s++ {
			dk := dists[s-1]
			if dk < 0.5 {
				dk = last
			}
			last = dk
			out = append(out, models.PlannedSession{
				Week:       w,
				Session:    s,
				DistanceKm: dk,
				Summary:    fmt.Sprintf("Semaine %d · séance %d (~%.1f km)", w, s, dk),
			})
		}
	}
	return out
}
