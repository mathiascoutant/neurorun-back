package goalcalendar

import (
	"math"
	"regexp"
	"strconv"
	"strings"
)

var (
	summaryKmRe = regexp.MustCompile(`(?i)(\d+(?:[.,]\d+)?)\s*km\b`)
	// Le mot qui introduit la distance part avec elle : « Footing facile de 5 km ».
	summaryKmLeadRe = regexp.MustCompile(`(?i)\s*(?:\(\s*)?(?:(?:de|sur|d['’]environ|environ)\s+|~\s*)?$`)
	summarySpacesRe = regexp.MustCompile(`\s{2,}`)
)

// SummaryForDistance retire du titre d'une séance une distance qui contredit sa
// distance totale. Le plan titre souvent sur le corps de séance (« Footing facile
// de 5 km ») alors que la distance affichée à côté compte échauffement et retour au
// calme (7 km) : les deux chiffres côte à côte se contredisent. La distance a déjà
// sa place, le titre garde la nature de la séance.
//
// Les répétitions d'un fractionné (« 3x2 km ») ne sont pas une distance de sortie
// et restent ; un titre qui cite plusieurs distances n'est pas touché.
func SummaryForDistance(summary string, totalKm float64) string {
	if totalKm <= 0 {
		return summary
	}
	var hit []int
	for _, m := range summaryKmRe.FindAllStringSubmatchIndex(summary, -1) {
		before := strings.TrimRight(summary[:m[0]], " ")
		if strings.HasSuffix(before, "x") || strings.HasSuffix(before, "X") || strings.HasSuffix(before, "×") {
			continue
		}
		if hit != nil {
			return summary
		}
		hit = m
	}
	if hit == nil {
		return summary
	}
	km, err := strconv.ParseFloat(strings.Replace(summary[hit[2]:hit[3]], ",", ".", 1), 64)
	if err != nil || math.Abs(km-totalKm) <= 0.5 {
		return summary
	}

	head := summary[:hit[0]]
	head = head[:summaryKmLeadRe.FindStringIndex(head)[0]]
	tail := summary[hit[1]:]
	if strings.Contains(summary[len(head):hit[0]], "(") {
		tail = strings.TrimPrefix(strings.TrimLeft(tail, " "), ")")
	}
	out := summarySpacesRe.ReplaceAllString(head+" "+strings.TrimLeft(tail, " "), " ")
	out = strings.Trim(out, " ·-–—:,")
	if out == "" {
		return summary
	}
	return out
}
