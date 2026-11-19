package goalcalendar

import (
	"fmt"
	"math"
	"sort"
	"time"

	"runapp/internal/models"
	"runapp/internal/strava"
)

// CalendarItem est une séance affichée + état Strava.
type CalendarItem struct {
	Date               string   `json:"date"`
	Week               int      `json:"week"`
	Session            int      `json:"session"`
	Summary            string   `json:"summary"`
	PlannedKm          float64  `json:"planned_km"`
	TargetPaceSecPerKm *float64 `json:"target_pace_sec_per_km,omitempty"`
	// Status : upcoming | done | partial | missed | skipped.
	Status string `json:"status"`
	// Rescheduled : la séance n'est pas au jour du motif hebdomadaire (report
	// demandé au coach, ou décalage suite à une indisponibilité).
	Rescheduled bool `json:"rescheduled,omitempty"`
	// PlannedDate : jour d'origine, renseigné seulement si la séance a bougé.
	PlannedDate        string   `json:"planned_date,omitempty"`
	Reason             string   `json:"reason,omitempty"`
	StravaActivityID   *int64   `json:"strava_activity_id,omitempty"`
	ActualKm           *float64 `json:"actual_km,omitempty"`
	ActualPaceSecPerKm *float64 `json:"actual_pace_sec_per_km,omitempty"`
	// Séance à intervalles : l'allure moyenne ci-dessus inclut les récupérations,
	// c'est `EffortPaceSecPerKm` qui a été comparée à la cible.
	IsInterval         bool     `json:"is_interval,omitempty"`
	EffortPaceSecPerKm *float64 `json:"effort_pace_sec_per_km,omitempty"`
	// LiveRunID : course NeuroRun lancée depuis cette séance et qui la remplit.
	LiveRunID string `json:"live_run_id,omitempty"`
	// RunDate : jour où la séance a réellement été courue, seulement s'il
	// diffère de `Date` (lancée la veille ou le lendemain).
	RunDate string `json:"run_date,omitempty"`
	// Kind : nature de la séance (easy, long, interval, tempo, hills, recovery, race).
	Kind string `json:"kind,omitempty"`
	// Detail : déroulé complet de la séance, lu dans le plan ou réécrit par le coach.
	Detail string `json:"detail,omitempty"`
	// Edited : le coach a modifié cette séance depuis la rédaction du plan.
	Edited bool `json:"edited,omitempty"`
	// Race : la séance est une course prévue par la personne.
	Race *CalendarRace `json:"race,omitempty"`
	// ScoreTotal : note sur 100 de la course NeuroRun qui remplit la séance.
	ScoreTotal *int `json:"score_total,omitempty"`
}

// CalendarRace : la course qui tient lieu de séance ce jour-là.
type CalendarRace struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	DistanceKm float64 `json:"distance_km"`
	TargetTime string  `json:"target_time,omitempty"`
	Official   bool    `json:"official,omitempty"`
}

// ResolvedPlannedSessions: stockées en base ou repli regex sur le Markdown.
func ResolvedPlannedSessions(g *models.Goal) []models.PlannedSession {
	if len(g.PlannedSessions) > 0 {
		return g.PlannedSessions
	}
	return FallbackPlannedSessionsFromPlan(g.Plan, g.Weeks, g.SessionsPerWeek)
}

// BuildCalendarItems fusionne objectif + sorties Strava. `effortPace` est facultatif :
// il donne l'allure des efforts d'un fractionné, jugé sur elle et non sur sa moyenne.
//
// `linked` sont les courses NeuroRun lancées depuis une séance précise : elles
// comptent pour elle, même courues la veille ou le lendemain, et ne sont plus
// candidates pour la séance du jour où elles tombent.
//
// `free` sont les autres courses NeuroRun (montre ou téléphone, sans séance choisie) :
// elles valent une sortie du jour, pour que le suivi marche aussi sans Strava.
func BuildCalendarItems(
	g *models.Goal,
	runs []strava.RunActivity,
	linked []LinkedRun,
	free []LinkedRun,
	loc *time.Location,
	now time.Time,
	effortPace EffortPaceResolver,
) []CalendarItem {
	if g == nil || loc == nil {
		return nil
	}
	pm := PlannedByWeekSession(g.Weeks, g.SessionsPerWeek, ResolvedPlannedSessions(g))
	defKm := DefaultDistanceKm(g.DistanceKm)
	linkedBySession := make(map[string][]LinkedRun, len(linked))
	for _, l := range linked {
		k := sessionKey(l.Week, l.Session)
		linkedBySession[k] = append(linkedBySession[k], l)
	}
	runs = withoutLinkedCopies(runs, linked)
	runs, freeByID := withFreeLiveRuns(runs, free)
	texts := newPlanTexts(g)
	var items []CalendarItem
	// Sorties déjà comptées pour une séance, et séances restées sans sortie à leur
	// jour : la seconde passe leur cherche une sortie courue la veille ou le lendemain.
	used := map[int64]bool{}
	var unfilled []unfilledSession
	for _, sch := range ResolveSchedule(g, loc) {
		w, s := sch.Week, sch.Session
		ps, ok := pm[sessionKey(w, s)]
		if !ok {
			ps = models.PlannedSession{
				Week: w, Session: s, DistanceKm: defKm,
				Summary: fmt.Sprintf("Semaine %d · séance %d (~%.1f km)", w, s, defKm),
			}
		}
		if ps.Summary == "" {
			ps.Summary = fmt.Sprintf("Sem.%d séance %d (~%.1f km)", w, s, ps.DistanceKm)
		}
		if ps.RaceID == "" {
			ps.Summary = SummaryForDistance(ps.Summary, ps.DistanceKm)
		}

		item := CalendarItem{
			Week:               w,
			Session:            s,
			Summary:            ps.Summary,
			PlannedKm:          ps.DistanceKm,
			TargetPaceSecPerKm: ps.PaceSecPerKm,
			Reason:             sch.Reason,
			Kind:               SessionKind(ps),
			Detail:             texts.text(w, s),
			Edited:             ps.Edited,
		}
		if race := FindRace(g, ps.RaceID); race != nil {
			item.Race = &CalendarRace{
				ID: race.ID, Name: race.Name, DistanceKm: race.DistanceKm,
				TargetTime: race.TargetTime, Official: race.Official,
			}
		}
		if sch.Moved {
			item.Rescheduled = true
			item.PlannedDate = DateKey(sch.PlannedDate)
		}

		// Une séance annulée reste visible à son jour d'origine : la personne doit
		// pouvoir constater ce qui a été retiré, sans qu'on lui compte un échec.
		if sch.Skipped {
			item.Date = DateKey(sch.PlannedDate)
			item.Status = "skipped"
			items = append(items, item)
			continue
		}

		day := sch.Date
		item.Date = DateKey(day)

		// Une course trop courte pour la séance ne la remplit pas (sauf fractionné
		// aux répétitions faites) : on retombe alors sur les sorties du jour, comme
		// si elle n'avait pas été lancée.
		if l, st := pickLinkedRun(linkedBySession[sessionKey(w, s)], ps); l != nil {
			item.Status = st
			item.LiveRunID = l.LiveRunID
			item.ScoreTotal = l.ScoreTotal
			if rd := DateKey(MidnightIn(l.Run.StartAt, loc)); rd != item.Date {
				item.RunDate = rd
			}
			fillActual(&item, l.Run, nil)
			items = append(items, item)
			continue
		}

		st, matched := SessionStatus(now, day, loc, runs, ps.DistanceKm, ps.PaceSecPerKm, effortPace)
		item.Status = st
		if matched == nil {
			if item.Race == nil {
				unfilled = append(unfilled, unfilledSession{idx: len(items), ps: ps})
			}
		} else {
			used[matched.ID] = true
			attachRun(&item, *matched, freeByID, effortPace)
		}
		items = append(items, item)
	}
	fillShiftedRuns(items, unfilled, runs, used, freeByID, loc, now, effortPace)
	return items
}

type unfilledSession struct {
	idx int
	ps  models.PlannedSession
}

// attachRun reporte sur la séance la sortie qui la remplit. Un identifiant négatif
// désigne une course NeuroRun courue sans passer par le bouton de la séance : elle
// compte quand même, Strava ou pas.
func attachRun(item *CalendarItem, r strava.RunActivity, freeByID map[int64]LinkedRun, effortPace EffortPaceResolver) {
	if r.ID > 0 {
		id := r.ID
		item.StravaActivityID = &id
	} else if l, ok := freeByID[r.ID]; ok {
		item.LiveRunID = l.LiveRunID
		item.ScoreTotal = l.ScoreTotal
	}
	fillActual(item, r, effortPace)
}

// fillShiftedRuns : la sortie longue du dimanche courue le samedi, sans passer par
// l'app, compte pour le dimanche. Une séance restée sans sortie à son jour prend
// une sortie de la veille ou du lendemain (même fenêtre qu'une course lancée depuis
// la séance), à condition qu'aucune autre séance ne l'ait déjà prise. La séance
// d'aujourd'hui ou de demain peut ainsi être remplie d'avance par une sortie déjà
// courue ; jamais par une sortie à venir.
//
// Quand une sortie pourrait servir deux séances, elle va à celle qu'elle valide
// plutôt qu'à celle qu'elle remplit à moitié, puis à celle dont la distance lui
// ressemble le plus : 10 km courus mercredi sont la sortie longue de jeudi, pas le
// fractionné manqué de mardi.
func fillShiftedRuns(
	items []CalendarItem,
	unfilled []unfilledSession,
	runs []strava.RunActivity,
	used map[int64]bool,
	freeByID map[int64]LinkedRun,
	loc *time.Location,
	now time.Time,
	effortPace EffortPaceResolver,
) {
	type pair struct {
		u      unfilledSession
		run    strava.RunActivity
		status string
		gap    float64
	}
	soon := DateKey(MidnightIn(now, loc).AddDate(0, 0, LinkWindowDays))
	var pairs []pair
	for _, u := range unfilled {
		item := &items[u.idx]
		if item.Status != "missed" && !(item.Status == "upcoming" && item.Date <= soon) {
			continue
		}
		day, err := time.ParseInLocation("2006-01-02", item.Date, loc)
		if err != nil {
			continue
		}
		for _, d := range []int{-LinkWindowDays, LinkWindowDays} {
			for _, r := range runsOnLocalDate(runs, day.AddDate(0, 0, d), loc) {
				if used[r.ID] || r.StartAt.After(now) {
					continue
				}
				st := finalizeStatus(&r, u.ps.DistanceKm, u.ps.PaceSecPerKm, effortPace)
				if st == "missed" {
					continue
				}
				gap := 0.0
				if u.ps.DistanceKm > 0 {
					gap = math.Abs(r.DistanceM/1000-u.ps.DistanceKm) / u.ps.DistanceKm
				}
				pairs = append(pairs, pair{u: u, run: r, status: st, gap: gap})
			}
		}
	}
	sort.SliceStable(pairs, func(i, j int) bool {
		if (pairs[i].status == "done") != (pairs[j].status == "done") {
			return pairs[i].status == "done"
		}
		return pairs[i].gap < pairs[j].gap
	})
	filled := map[int]bool{}
	for _, p := range pairs {
		if filled[p.u.idx] || used[p.run.ID] {
			continue
		}
		filled[p.u.idx] = true
		used[p.run.ID] = true
		item := &items[p.u.idx]
		item.Status = p.status
		item.RunDate = DateKey(MidnightIn(p.run.StartAt, loc))
		attachRun(item, p.run, freeByID, effortPace)
	}
}

// fillActual reporte ce qui a été couru : distance, allure moyenne, et pour un
// fractionné l'allure des seuls efforts quand elle est connue.
func fillActual(item *CalendarItem, r strava.RunActivity, effortPace EffortPaceResolver) {
	km := r.DistanceM / 1000
	item.ActualKm = &km
	p := PaceSecPerKm(r)
	item.ActualPaceSecPerKm = &p
	if IsInterval(r) {
		item.IsInterval = true
		if ep, _ := JudgedPaceSecPerKm(r, effortPace); ep > 0 {
			item.EffortPaceSecPerKm = &ep
		}
	}
}
