package goalcalendar

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"

	"runapp/internal/models"
)

var (
	reKindInterval = regexp.MustCompile(`(?i)fractionn|interval|r[ée]p[ée]t|\d+\s*[x×]\s*\d|vma`)
	reKindLong     = regexp.MustCompile(`(?i)sortie longue|longue`)
	reKindTempo    = regexp.MustCompile(`(?i)seuil|tempo|allure (course|sp[ée]cifique|marathon|semi|10)`)
	reKindHills    = regexp.MustCompile(`(?i)c[ôo]tes?\b|d[ée]nivel|mont[ée]es`)
	reKindRecovery = regexp.MustCompile(`(?i)r[ée]cup[ée]ration|d[ée]crassage`)
	reKindEasy     = regexp.MustCompile(`(?i)footing|facile|endurance|l[ée]g[èe]re?`)
)

// SessionKind : la nature d'une séance. Les plans d'avant le champ `kind` n'en
// portent pas : elle se devine alors dans le résumé, dans un ordre qui met le
// travail spécifique devant les mots passe-partout (« endurance » figure presque
// partout, y compris dans l'échauffement d'un fractionné).
func SessionKind(ps models.PlannedSession) string {
	if ps.RaceID != "" {
		return models.SessionKindRace
	}
	if k := models.NormalizeSessionKind(ps.Kind); k != "" {
		return k
	}
	text := ps.Summary
	switch {
	case reKindInterval.MatchString(text):
		return models.SessionKindInterval
	case reKindHills.MatchString(text):
		return models.SessionKindHills
	case reKindTempo.MatchString(text):
		return models.SessionKindTempo
	case reKindLong.MatchString(text):
		return models.SessionKindLong
	case reKindRecovery.MatchString(text):
		return models.SessionKindRecovery
	case reKindEasy.MatchString(text):
		return models.SessionKindEasy
	}
	return ""
}

// planTexts donne le déroulé de chaque séance : la version réécrite par le coach
// quand elle existe, sinon le texte du plan Markdown.
type planTexts struct {
	blocks  map[int]string
	details map[string]string
}

func newPlanTexts(g *models.Goal) planTexts {
	pt := planTexts{blocks: planWeekBlocks(g.Plan), details: map[string]string{}}
	for _, p := range g.PlannedSessions {
		if d := strings.TrimSpace(p.Detail); d != "" {
			pt.details[sessionKey(p.Week, p.Session)] = d
		}
	}
	return pt
}

func (pt planTexts) text(week, session int) string {
	if d, ok := pt.details[sessionKey(week, session)]; ok {
		return d
	}
	if it, ok := sessionAt(weekSessions(pt.blocks[week]), session); ok {
		return it.text
	}
	return ""
}

// SessionText : le déroulé d'une séance, tel que la personne doit le lire.
func SessionText(g *models.Goal, week, session int) string {
	if g == nil {
		return ""
	}
	return newPlanTexts(g).text(week, session)
}

// MaterializePlanned garantit une ligne par séance dans g.PlannedSessions. Les
// vieux objectifs n'en stockent pas (elles sont relues dans le Markdown à chaque
// fois) : impossible alors de modifier une séance sans figer les autres d'abord.
func MaterializePlanned(g *models.Goal) {
	byKey := PlannedByWeekSession(g.Weeks, g.SessionsPerWeek, ResolvedPlannedSessions(g))
	defKm := DefaultDistanceKm(g.DistanceKm)
	out := make([]models.PlannedSession, 0, g.Weeks*g.SessionsPerWeek)
	for w := 1; w <= g.Weeks; w++ {
		for s := 1; s <= g.SessionsPerWeek; s++ {
			ps, ok := byKey[sessionKey(w, s)]
			if !ok {
				ps = models.PlannedSession{Week: w, Session: s, DistanceKm: defKm}
			}
			out = append(out, ps)
		}
	}
	g.PlannedSessions = out
}

// PlannedAt renvoie la séance stockée (semaine, numéro), nil si elle n'existe pas.
func PlannedAt(g *models.Goal, week, session int) *models.PlannedSession {
	for i := range g.PlannedSessions {
		if g.PlannedSessions[i].Week == week && g.PlannedSessions[i].Session == session {
			return &g.PlannedSessions[i]
		}
	}
	return nil
}

// FindRace renvoie la course prévue portant cet identifiant.
func FindRace(g *models.Goal, id string) *models.GoalRace {
	if g == nil || id == "" {
		return nil
	}
	for i := range g.Races {
		if g.Races[i].ID == id {
			return &g.Races[i]
		}
	}
	return nil
}

// EndDate : dernier jour de la préparation — le jour de la course s'il est connu,
// sinon le dimanche de la dernière semaine du plan.
func EndDate(g *models.Goal, loc *time.Location) time.Time {
	if d, ok := ParseDateKey(g.RaceDate, loc); ok {
		return d
	}
	return MondayContaining(g.CreatedAt, loc).AddDate(0, 0, g.Weeks*7-1)
}

// WeekOf : numéro de semaine du plan (1 = première) où tombe ce jour ; 0 hors prépa.
func WeekOf(g *models.Goal, day time.Time, loc *time.Location) int {
	start := MondayContaining(g.CreatedAt, loc)
	d := civilDayDiff(day, start, loc)
	if d < 0 {
		return 0
	}
	w := d/7 + 1
	if w > g.Weeks {
		return 0
	}
	return w
}

// ErrRaceNoSlot : aucune séance à venir ne peut céder sa place à la course.
var ErrRaceNoSlot = errors.New("aucune séance à remplacer cette semaine-là")

// AttachRace inscrit une course dans la préparation : elle prend la place de la
// séance la plus proche de sa date, dans la même semaine du plan, et cette séance
// se cale sur le jour de la course. Une préparation garde ainsi son nombre de
// séances — courir un 10 km le dimanche ne s'ajoute pas à la sortie longue, il la
// remplace.
func AttachRace(g *models.Goal, race models.GoalRace, loc *time.Location, now time.Time) (models.GoalRace, error) {
	day, ok := ParseDateKey(race.Date, loc)
	if !ok {
		return race, errors.New("date de course attendue au format AAAA-MM-JJ")
	}
	today := MidnightIn(now, loc)
	if day.Before(today) {
		return race, errors.New("cette date est déjà passée")
	}
	if race.DistanceKm < 1 || race.DistanceKm > 250 {
		return race, errors.New("distance de course invalide")
	}
	if end := EndDate(g, loc); !day.Before(end) && strings.TrimSpace(g.RaceDate) != "" {
		return race, errors.New("cette course tombe le jour de l'objectif ou après : c'est l'objectif lui-même")
	}
	week := WeekOf(g, day, loc)
	if week == 0 {
		return race, errors.New("cette date est en dehors de la préparation")
	}
	for _, r := range g.Races {
		if r.Date == DateKey(day) {
			return race, errors.New("une course est déjà prévue ce jour-là")
		}
	}

	MaterializePlanned(g)
	var chosen *ScheduledSession
	bestGap := math.MaxInt
	sched := ResolveSchedule(g, loc)
	for i := range sched {
		s := &sched[i]
		if s.Week != week || s.Skipped || s.Date.IsZero() || s.Date.Before(today) {
			continue
		}
		if ps := PlannedAt(g, s.Week, s.Session); ps == nil || ps.RaceID != "" {
			continue
		}
		gap := civilDayDiff(s.Date, day, loc)
		if gap < 0 {
			gap = -gap
		}
		// À distance égale, la séance la plus tardive cède sa place : c'est le plus
		// souvent la sortie longue du week-end, celle qu'une course remplace le mieux.
		if gap < bestGap || (gap == bestGap && chosen != nil && s.Session > chosen.Session) {
			bestGap = gap
			chosen = s
		}
	}
	if chosen == nil {
		return race, ErrRaceNoSlot
	}

	ps := PlannedAt(g, chosen.Week, chosen.Session)
	original := *ps
	original.Steps = nil
	race.Week, race.Session = chosen.Week, chosen.Session
	race.Date = DateKey(day)
	race.Replaced = &original

	ps.DistanceKm = math.Round(race.DistanceKm*10) / 10
	ps.PaceSecPerKm = nil
	ps.Summary = "Course : " + race.Name
	ps.Kind = models.SessionKindRace
	ps.Detail = raceDetail(race)
	ps.Edited = true
	ps.RaceID = race.ID
	ps.Steps = nil

	upsertOverride(g, models.SessionOverride{
		Week: chosen.Week, Session: chosen.Session, Date: race.Date, Reason: "Course : " + race.Name,
	})
	g.Races = append(g.Races, race)
	return race, nil
}

// DetachRace retire une course : la séance qu'elle remplaçait revient, à sa date.
func DetachRace(g *models.Goal, id string) (models.GoalRace, bool) {
	idx := -1
	for i := range g.Races {
		if g.Races[i].ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return models.GoalRace{}, false
	}
	race := g.Races[idx]
	g.Races = append(g.Races[:idx], g.Races[idx+1:]...)

	if ps := PlannedAt(g, race.Week, race.Session); ps != nil && ps.RaceID == id {
		if race.Replaced != nil {
			*ps = *race.Replaced
		} else {
			ps.RaceID, ps.Kind, ps.Detail, ps.Edited, ps.Steps = "", "", "", false, nil
		}
	}
	kept := g.SessionOverrides[:0]
	for _, o := range g.SessionOverrides {
		if o.Week == race.Week && o.Session == race.Session && !o.Skipped {
			continue
		}
		kept = append(kept, o)
	}
	g.SessionOverrides = kept
	return race, true
}

// ReattachRaces remet les courses prévues dans un plan qui vient d'être réécrit :
// la réécriture repart de séances neuves et ne connaît que le texte.
func ReattachRaces(g *models.Goal, loc *time.Location, now time.Time) {
	races := g.Races
	g.Races = nil
	for _, r := range races {
		r.Replaced = nil
		attached, err := AttachRace(g, r, loc, now)
		if err != nil {
			// Course passée ou sortie de la prépa : elle reste listée, sans séance.
			r.Week, r.Session = 0, 0
			g.Races = append(g.Races, r)
			continue
		}
		_ = attached
	}
}

func upsertOverride(g *models.Goal, o models.SessionOverride) {
	for i := range g.SessionOverrides {
		if g.SessionOverrides[i].Week == o.Week && g.SessionOverrides[i].Session == o.Session {
			g.SessionOverrides[i] = o
			return
		}
	}
	g.SessionOverrides = append(g.SessionOverrides, o)
}

func raceDetail(r models.GoalRace) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Course « %s », %s km", r.Name, strings.Replace(fmt.Sprintf("%.1f", r.DistanceKm), ".", ",", 1))
	if t := strings.TrimSpace(r.TargetTime); t != "" {
		fmt.Fprintf(&b, ", objectif %s", t)
	}
	b.WriteString(". Échauffement de 10 à 15 min très facile, puis quelques accélérations courtes avant le départ. " +
		"Pars un peu en dessous de ton allure visée sur le premier quart, puis installe-toi. " +
		"Après l'arrivée, marche puis trottine 5 à 10 min pour récupérer.")
	return b.String()
}
