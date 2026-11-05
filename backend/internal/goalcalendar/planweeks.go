package goalcalendar

import (
	"context"
	"errors"
	"regexp"
	"strconv"
	"strings"
)

/*
 * Complétude du calendrier d'un plan.
 *
 * Dès qu'une préparation dépasse quelques semaines, le modèle s'arrête en route et
 * conclut par « répéter une structure similaire pour les semaines suivantes », même
 * quand on lui demande tout. Le rappeler dans le prompt ne suffit pas : on vérifie
 * donc le texte reçu, et on lui fait écrire ce qui manque par petits lots.
 */

// planWeeksBatch : semaines demandées d'un coup. Le modèle tient ce volume sans résumer.
const planWeeksBatch = 5

// planWeeksMaxStalls : lots d'affilée sans nouvelle semaine avant de renoncer.
const planWeeksMaxStalls = 2

// ErrIncompletePlan : le modèle n'a pas écrit toutes les semaines malgré les relances.
var ErrIncompletePlan = errors.New("plan incomplet")

var (
	rePlanWeekTitle   = regexp.MustCompile(`(?mi)^###\s*Semaine\s*(\d+)[^\n]*$`)
	rePlanLevel2Title = regexp.MustCompile(`(?m)^##\s+\S`)
	rePlanCalendar    = regexp.MustCompile(`(?mi)^##\s+Calendrier[^\n]*\n?`)
	rePlanAfterWeeks  = regexp.MustCompile(`(?mi)^##\s+Dans les derniers`)
	// Le libellé varie d'un plan à l'autre : « - **Séance 1 :** », « Séance 1 : »,
	// « - **Séance 1** : » ou « #### **Séance 1 : Footing** ».
	reSessionLabel = regexp.MustCompile(`(?i)^[^\p{L}\p{N}]*S[ée]ance\s*(\d+)(.*)$`)
	// Renvoi à la place de séances écrites : à retirer une fois les semaines rédigées.
	rePlanFiller = regexp.MustCompile(`(?i)semaines?\s+suivantes|r[ée]p[ée]t\w*\s+(une|la|cette|ce|le)\b.*(structure|sch[ée]ma|semaine)|m[êe]me\s+structure|et\s+ainsi\s+de\s+suite`)
)

// planWeekSpan situe une semaine dans le texte : de son titre à la semaine ou à la
// section suivante.
type planWeekSpan struct {
	week       int
	start, end int
}

func planWeekSpans(text string) []planWeekSpan {
	titles := rePlanWeekTitle.FindAllStringSubmatchIndex(text, -1)
	spans := make([]planWeekSpan, 0, len(titles))
	for i, loc := range titles {
		w, err := strconv.Atoi(text[loc[2]:loc[3]])
		if err != nil {
			continue
		}
		end := len(text)
		if i+1 < len(titles) {
			end = titles[i+1][0]
		}
		if next := rePlanLevel2Title.FindStringIndex(text[loc[1]:end]); next != nil {
			end = loc[1] + next[0]
		}
		spans = append(spans, planWeekSpan{week: w, start: loc[0], end: end})
	}
	return spans
}

// planSession : une séance telle qu'écrite dans le plan, sans son libellé.
type planSession struct {
	number int
	text   string
}

// weekSessions relit les séances d'une semaine. Le corps d'une séance peut courir sur
// plusieurs lignes (« liste courte » du prompt) : elles sont rattachées à son libellé.
func weekSessions(block string) []planSession {
	var out []planSession
	seen := map[int]bool{}
	current := -1
	for _, line := range strings.Split(block, "\n") {
		line = strings.TrimSpace(line)
		if m := reSessionLabel.FindStringSubmatch(line); m != nil {
			n, err := strconv.Atoi(m[1])
			if err != nil || seen[n] {
				current = -1
				continue
			}
			seen[n] = true
			out = append(out, planSession{number: n, text: cleanSessionText(m[2])})
			current = len(out) - 1
			continue
		}
		if current < 0 || line == "" || rePlanFiller.MatchString(line) {
			continue
		}
		if rest := cleanSessionText(line); rest != "" {
			if out[current].text != "" {
				out[current].text += " "
			}
			out[current].text += rest
		}
	}
	return out
}

func cleanSessionText(s string) string {
	s = strings.ReplaceAll(s, "**", "")
	return strings.Trim(s, " \t*_:—–-")
}

// planWeekBlocks : le texte de chaque semaine, la première occurrence faisant foi.
func planWeekBlocks(text string) map[int]string {
	out := make(map[int]string)
	for _, sp := range planWeekSpans(text) {
		if _, dup := out[sp.week]; !dup {
			out[sp.week] = text[sp.start:sp.end]
		}
	}
	return out
}

// MissingPlanWeeks liste les semaines absentes du plan, ou écrites avec moins de
// séances que prévu. Vide quand le plan est complet.
func MissingPlanWeeks(plan string, weeks, spw int) []int {
	blocks := planWeekBlocks(plan)
	var missing []int
	for w := 1; w <= weeks; w++ {
		if len(weekSessions(blocks[w])) < spw {
			missing = append(missing, w)
		}
	}
	return missing
}

// mergePlanWeeks range dans le plan les semaines `wanted` trouvées dans `written`.
// Une semaine incomplète est ignorée : elle reste à demander. Le reste du plan ne
// bouge pas, hors renvois du type « répéter pour les semaines suivantes ».
func mergePlanWeeks(plan, written string, wanted []int, weeks, spw int) string {
	blocks := planWeekBlocks(plan)
	newBlocks := planWeekBlocks(written)
	for _, w := range wanted {
		if b, ok := newBlocks[w]; ok && len(weekSessions(b)) >= spw {
			blocks[w] = b
		}
	}

	var calendar strings.Builder
	for w := 1; w <= weeks; w++ {
		b, ok := blocks[w]
		if !ok {
			continue
		}
		if calendar.Len() > 0 {
			calendar.WriteString("\n\n")
		}
		calendar.WriteString(withoutFiller(b))
	}
	if calendar.Len() == 0 {
		return plan
	}

	head, tail := splitAroundWeeks(plan)
	return head + calendar.String() + "\n\n" + tail
}

// splitAroundWeeks coupe le plan autour de son calendrier. Sans aucune semaine écrite,
// le calendrier se pose sous son titre, ou à défaut avant la fin de préparation.
func splitAroundWeeks(plan string) (head, tail string) {
	if spans := planWeekSpans(plan); len(spans) > 0 {
		return plan[:spans[0].start], plan[spans[len(spans)-1].end:]
	}
	if loc := rePlanCalendar.FindStringIndex(plan); loc != nil {
		head, tail = plan[:loc[1]], plan[loc[1]:]
		if !strings.HasSuffix(head, "\n") {
			head += "\n"
		}
		return head, strings.TrimLeft(tail, "\n")
	}
	const title = "## Calendrier — semaine par semaine\n"
	if loc := rePlanAfterWeeks.FindStringIndex(plan); loc != nil {
		return plan[:loc[0]] + title, plan[loc[0]:]
	}
	return strings.TrimRight(plan, "\n") + "\n\n" + title, ""
}

func withoutFiller(block string) string {
	lines := strings.Split(block, "\n")
	kept := lines[:0]
	for _, line := range lines {
		if rePlanFiller.MatchString(line) && reSessionLabel.FindString(strings.TrimSpace(line)) == "" {
			continue
		}
		kept = append(kept, line)
	}
	return strings.TrimRight(strings.Join(kept, "\n"), " \t\n")
}

// CompletePlanWeeks fait écrire les semaines manquantes jusqu'à ce que le plan soit
// complet. `write` reçoit le plan tel qu'il est et les semaines à rédiger ; `onProgress`
// (facultatif) est appelé après chaque lot utile. Un plan déjà complet est rendu tel quel.
func CompletePlanWeeks(
	ctx context.Context,
	plan string,
	weeks, spw int,
	write func(ctx context.Context, plan string, wanted []int) (string, error),
	onProgress func(plan string),
) (string, error) {
	stalls := 0
	missing := MissingPlanWeeks(plan, weeks, spw)
	for len(missing) > 0 {
		wanted := missing
		if len(wanted) > planWeeksBatch {
			wanted = wanted[:planWeeksBatch]
		}
		written, err := write(ctx, plan, wanted)
		if err != nil {
			return "", err
		}
		merged := mergePlanWeeks(plan, written, wanted, weeks, spw)
		left := MissingPlanWeeks(merged, weeks, spw)
		if len(left) >= len(missing) {
			stalls++
			if stalls >= planWeeksMaxStalls {
				return "", ErrIncompletePlan
			}
			continue
		}
		stalls = 0
		plan, missing = merged, left
		if onProgress != nil {
			onProgress(plan)
		}
	}
	return plan, nil
}
