package shoes

import (
	"fmt"
	"math"
	"sort"
	"time"
)

// Profils de séance : la seule chose qui décide de la paire. Le détail du type
// de séance (seuil, allure spécifique, VMA…) ne change rien au choix.
const (
	ProfileEasy  = "easy"  // endurance, sortie longue, récupération
	ProfileFast  = "fast"  // fractionné, seuil, allure spécifique, course à fond
	ProfileHilly = "hilly" // trail, côtes, gros dénivelé
)

// ValidProfile dit si un profil de séance est connu.
func ValidProfile(p string) bool {
	return p == ProfileEasy || p == ProfileFast || p == ProfileHilly
}

// Seuils du profil vallonné : 300 m de D+ quelle que soit la distance, ou
// 25 m par kilomètre sur une sortie d'au moins 3 km (une boucle de 6 km avec
// 180 m de D+ se court déjà comme un trail).
const (
	hillyGainM       = 300.0
	hillyGainPerKm   = 25.0
	hillyMinDistKm   = 3.0
	habitMinRuns     = 2
	habitMinShare    = 0.5
	freshWearPct     = 75.0 // au-delà, la paire est « à surveiller »
	spareWearPct     = 90.0 // au-delà, on conseille de la ménager
	exhaustedWearPct = 100.0
)

// RunProfile : profil d'une course terminée. runType est le type choisi au
// départ (free, endurance, interval), scored celui retenu par la note
// (endurance, tempo, race, interval ; vide sans note).
func RunProfile(runType, scored string, distanceKm, gainM float64) string {
	if gainM >= hillyGainM || (distanceKm >= hillyMinDistKm && gainM/distanceKm >= hillyGainPerKm) {
		return ProfileHilly
	}
	if runType == "interval" {
		return ProfileFast
	}
	switch scored {
	case "interval", "tempo", "race":
		return ProfileFast
	}
	return ProfileEasy
}

// Candidate : une paire active, telle que la suggestion la lit.
type Candidate struct {
	ID string
	// Name : comment la nommer dans une phrase (surnom, sinon modèle).
	Name     string
	Category string
	WearPct  float64
}

// PastRun : une course passée déjà rattachée à une paire.
type PastRun struct {
	ShoeID  string
	Profile string
	Weekday time.Weekday
}

// Suggestion : la paire proposée et pourquoi, en une phrase courte.
type Suggestion struct {
	ShoeID string `json:"shoe_id"`
	Reason string `json:"reason"`
	// Tip : conseil de rotation, seulement quand l'usure le justifie.
	Tip string `json:"tip,omitempty"`
}

var weekdayNames = [...]string{"dimanche", "lundi", "mardi", "mercredi", "jeudi", "vendredi", "samedi"}

var profileHabit = map[string]string{
	ProfileEasy:  "Ta paire habituelle en endurance",
	ProfileFast:  "Ta paire habituelle pour les séances rapides",
	ProfileHilly: "Ta paire habituelle sur les sorties vallonnées",
}

// Guess : la paire que le coureur a le plus probablement portée. Les habitudes
// passent avant le catalogue — celui qui court ses fractionnés en Pegasus le
// fait exprès — puis la catégorie, puis la paire la plus portée. Rien quand
// aucun indice ne départage : mieux vaut ne rien cocher que cocher faux.
func Guess(profile string, day time.Weekday, cands []Candidate, history []PastRun) *Suggestion {
	if len(cands) == 0 {
		return nil
	}
	known := map[string]bool{}
	for _, c := range cands {
		known[c.ID] = true
	}
	var sameProfile, sameDay, all []PastRun
	for _, r := range history {
		if !known[r.ShoeID] {
			continue
		}
		all = append(all, r)
		if r.Profile == profile {
			sameProfile = append(sameProfile, r)
			if r.Weekday == day {
				sameDay = append(sameDay, r)
			}
		}
	}

	// Une sortie facile du mardi ressemble surtout aux autres sorties faciles
	// du mardi (le footing du club, la paire laissée au bureau…).
	if profile == ProfileEasy {
		if id := habit(sameDay); id != "" {
			return &Suggestion{ShoeID: id, Reason: "Celle que tu portes d’habitude le " + weekdayNames[day]}
		}
	}
	if id := habit(sameProfile); id != "" {
		return &Suggestion{ShoeID: id, Reason: profileHabit[profile]}
	}
	if s := byCategory(profile, cands); s != nil {
		return s
	}
	if id := habit(all); id != "" {
		return &Suggestion{ShoeID: id, Reason: "Celle que tu portes le plus souvent"}
	}
	return nil
}

// Advise : la paire à porter pour une séance à venir. Même lecture que Guess,
// puis l'usure : on ne conseille pas une paire épuisée pour une séance qui
// demande du répondant, et on invite à alterner avant qu'elle ne lâche.
func Advise(profile string, day time.Weekday, cands []Candidate, history []PastRun) *Suggestion {
	s := Guess(profile, day, cands, history)
	if s == nil {
		return nil
	}
	pick := find(cands, s.ShoeID)
	alt := fresher(profile, cands, pick)
	if alt == nil {
		return s
	}
	switch {
	case pick.WearPct >= exhaustedWearPct:
		return &Suggestion{
			ShoeID: alt.ID,
			Reason: "Plus fraîche que " + article(pick.Name),
			Tip:    fmt.Sprintf("%s a dépassé sa durée de vie théorique : place à %s.", capital(article(pick.Name)), article(alt.Name)),
		}
	case profile == ProfileFast && pick.WearPct >= freshWearPct:
		// Une paire de compétition fatiguée se garde pour le jour J, une
		// paire d'entraînement pour les sorties tranquilles.
		keep := "l’endurance"
		if pick.Category == CategoryRace {
			keep = "le jour de course"
		}
		return &Suggestion{
			ShoeID: alt.ID,
			Reason: "Plus de répondant pour une séance rapide",
			Tip: fmt.Sprintf("%s est à %d %% : garde-la pour %s et sors %s pour le fractionné.",
				capital(article(pick.Name)), int(math.Round(pick.WearPct)), keep, article(alt.Name)),
		}
	case pick.WearPct >= spareWearPct:
		s.Tip = fmt.Sprintf("%s est à %d %% : alterne avec %s pour la ménager jusqu’à la relève.",
			capital(article(pick.Name)), int(math.Round(pick.WearPct)), article(alt.Name))
	}
	return s
}

// habit : la paire portée sur la majorité de ces courses, s'il y en a assez
// pour parler d'habitude. Vide sinon.
func habit(runs []PastRun) string {
	if len(runs) < habitMinRuns {
		return ""
	}
	counts := map[string]int{}
	ids := []string{}
	for _, r := range runs {
		if counts[r.ShoeID] == 0 {
			ids = append(ids, r.ShoeID)
		}
		counts[r.ShoeID]++
	}
	sort.Slice(ids, func(i, j int) bool { return counts[ids[i]] > counts[ids[j]] })
	best, bestN := ids[0], counts[ids[0]]
	// À égalité, pas d'habitude : deux paires portées autant, c'est une rotation.
	if len(ids) > 1 && counts[ids[1]] == bestN {
		return ""
	}
	if bestN < habitMinRuns || float64(bestN)/float64(len(runs)) < habitMinShare {
		return ""
	}
	return best
}

// Catégories adaptées à chaque profil, de la plus évidente à la moins.
var profileCategories = map[string][][]string{
	ProfileHilly: {{CategoryTrail}},
	ProfileFast:  {{CategoryRace}, {CategoryTempo}},
	ProfileEasy:  {{CategoryDaily, CategoryCushion, CategoryStability}},
}

var categoryReason = map[string]string{
	CategoryTrail:     "Ta paire de trail, pour le dénivelé",
	CategoryRace:      "Ta paire de compétition, pour une séance rapide",
	CategoryTempo:     "Ta paire dynamique, pour une séance rapide",
	CategoryDaily:     "Ta paire d’entraînement",
	CategoryCushion:   "Ta paire d’entraînement",
	CategoryStability: "Ta paire d’entraînement",
}

// byCategory : la seule paire active de la catégorie qui convient. Deux paires
// de trail, c'est une question ouverte : on ne tranche pas au hasard.
func byCategory(profile string, cands []Candidate) *Suggestion {
	for _, group := range profileCategories[profile] {
		var match []Candidate
		for _, c := range cands {
			for _, cat := range group {
				if c.Category == cat {
					match = append(match, c)
				}
			}
		}
		if len(match) == 1 {
			return &Suggestion{ShoeID: match[0].ID, Reason: categoryReason[match[0].Category]}
		}
		if len(match) > 1 {
			return nil
		}
	}
	return nil
}

// fresher : une autre paire nettement moins usée, adaptée au profil si
// possible. Nil s'il n'y en a pas.
func fresher(profile string, cands []Candidate, pick *Candidate) *Candidate {
	if pick == nil {
		return nil
	}
	var pool []Candidate
	for _, c := range cands {
		if c.ID != pick.ID && c.WearPct < freshWearPct && c.WearPct < pick.WearPct {
			pool = append(pool, c)
		}
	}
	if len(pool) == 0 {
		return nil
	}
	fits := map[string]bool{}
	for _, group := range profileCategories[profile] {
		for _, cat := range group {
			fits[cat] = true
		}
	}
	// Une paire de compétition ne remplace pas une paire d'entraînement : en
	// endurance, on n'en propose que si rien d'autre n'est disponible.
	sort.SliceStable(pool, func(i, j int) bool {
		if fits[pool[i].Category] != fits[pool[j].Category] {
			return fits[pool[i].Category]
		}
		return pool[i].WearPct < pool[j].WearPct
	})
	if profile == ProfileEasy && !fits[pool[0].Category] {
		return nil
	}
	return &pool[0]
}

func find(cands []Candidate, id string) *Candidate {
	for i := range cands {
		if cands[i].ID == id {
			return &cands[i]
		}
	}
	return nil
}

// article : « ta Pegasus 41 ». On parle d'une paire : « ta » vaut pour
// n'importe quel modèle ou surnom.
func article(name string) string {
	return "ta " + name
}

func capital(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	if r[0] >= 'a' && r[0] <= 'z' {
		r[0] -= 'a' - 'A'
	}
	return string(r)
}
