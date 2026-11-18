package handlers

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"

	"runapp/internal/goalcalendar"
	"runapp/internal/models"
	"runapp/internal/strava"
	"runapp/internal/vma"
)

// goalRuns : ce que la personne a couru depuis le début de sa préparation, lu une
// fois par requête. Le calendrier se reconstruit dessus autant de fois que le coach
// modifie le plan, sans réinterroger Strava.
type goalRuns struct {
	strava []strava.RunActivity
	// linked : courses NeuroRun lancées depuis une séance de cet objectif.
	linked []goalcalendar.LinkedRun
	// free : les autres courses NeuroRun, qui valent une sortie du jour.
	free   []goalcalendar.LinkedRun
	effort goalcalendar.EffortPaceResolver
}

func (gr goalRuns) items(g *models.Goal, loc *time.Location, now time.Time) []goalcalendar.CalendarItem {
	return goalcalendar.BuildCalendarItems(g, gr.strava, gr.linked, gr.free, loc, now, gr.effort)
}

// loadGoalRuns lit les sorties Strava et les courses NeuroRun de la préparation.
//
// L'erreur Strava est rendue à part : la route du calendrier la remonte à l'app
// pour proposer de reconnecter le compte, alors que le coach et la synthèse s'en
// passent et travaillent avec les seules courses NeuroRun.
func (h *Handlers) loadGoalRuns(r *http.Request, u *models.User, g *models.Goal) (gr goalRuns, stravaErr error) {
	ctx := r.Context()
	if u.HasStrava() {
		access, err := h.ensureStravaAccess(ctx, u)
		if err != nil {
			stravaErr = err
		} else {
			after := g.CreatedAt.Unix() - 7200
			if gr.strava, err = h.strava.FetchRunActivities(ctx, access, &after); err != nil {
				stravaErr = err
				gr.strava = nil
			} else {
				gr.strava = withoutHiddenStrava(u, gr.strava)
			}
		}
	}
	if lives, err := h.db.ListLiveRunsSince(ctx, u.ID, g.CreatedAt.Add(-2*time.Hour)); err == nil {
		for _, lr := range lives {
			switch {
			case lr.GoalSession == nil:
				gr.free = append(gr.free, goalcalendar.RunFromLive(lr))
			case lr.GoalSession.GoalID == g.ID:
				gr.linked = append(gr.linked, goalcalendar.RunFromLive(lr))
			}
		}
	}
	if stravaErr == nil {
		gr.effort = h.effortPaceResolver(r, u)
	}
	return gr, stravaErr
}

// goalCalendarItems construit le calendrier d'un objectif : séances du plan, et
// pour chacune ce qui a réellement été couru (Strava et courses NeuroRun).
func (h *Handlers) goalCalendarItems(r *http.Request, u *models.User, g *models.Goal) ([]goalcalendar.CalendarItem, error) {
	gr, stravaErr := h.loadGoalRuns(r, u, g)
	loc, _ := calendarLocation()
	return gr.items(g, loc, time.Now().UTC()), stravaErr
}

// goalProgress : où en est la préparation, en chiffres. La synthèse du coach et
// l'écran Objectif partent des mêmes nombres, pour ne jamais se contredire.
type goalProgress struct {
	// Week : semaine du plan en cours (1 = première) ; 0 une fois la prépa finie.
	Week       int    `json:"week"`
	Weeks      int    `json:"weeks"`
	EndDate    string `json:"end_date"`
	DaysToRace int    `json:"days_to_race"`

	Total    int `json:"total"`
	Done     int `json:"done"`
	Partial  int `json:"partial"`
	Missed   int `json:"missed"`
	Skipped  int `json:"skipped"`
	Upcoming int `json:"upcoming"`
	// AdherencePct : part des séances échues réellement courues (une séance
	// partielle compte pour moitié, une séance annulée ne compte pas). -1 tant
	// qu'aucune séance n'est échue.
	AdherencePct int `json:"adherence_pct"`
	// RecentMissed : séances manquées sur les 14 derniers jours.
	RecentMissed int `json:"recent_missed"`

	KmDone         float64 `json:"km_done"`
	KmPlannedSoFar float64 `json:"km_planned_so_far"`
	KmPlannedTotal float64 `json:"km_planned_total"`

	WeekDone  int `json:"week_done"`
	WeekTotal int `json:"week_total"`
	// Tone : good | watch | alert — la couleur de la synthèse.
	Tone string `json:"tone"`
}

func computeGoalProgress(g *models.Goal, items []goalcalendar.CalendarItem, loc *time.Location, now time.Time) goalProgress {
	today := goalcalendar.MidnightIn(now, loc)
	end := goalcalendar.EndDate(g, loc)
	p := goalProgress{
		Week:         goalcalendar.WeekOf(g, today, loc),
		Weeks:        g.Weeks,
		EndDate:      goalcalendar.DateKey(end),
		DaysToRace:   int(math.Round(end.Sub(today).Hours() / 24)),
		Total:        len(items),
		AdherencePct: -1,
	}
	if p.DaysToRace < 0 {
		p.DaysToRace = 0
	}
	recentFrom := today.AddDate(0, 0, -14)
	due, credit := 0, 0.0
	for _, it := range items {
		day, _ := goalcalendar.ParseDateKey(it.Date, loc)
		inWeek := it.Week == p.Week
		if it.Status != "skipped" {
			p.KmPlannedTotal += it.PlannedKm
			if inWeek {
				p.WeekTotal++
			}
		}
		switch it.Status {
		case "done":
			p.Done++
			due++
			credit++
		case "partial":
			p.Partial++
			due++
			credit += 0.5
		case "missed":
			p.Missed++
			due++
			if !day.Before(recentFrom) {
				p.RecentMissed++
			}
		case "skipped":
			p.Skipped++
			continue
		default:
			p.Upcoming++
		}
		if it.Status == "done" || it.Status == "partial" {
			if inWeek {
				p.WeekDone++
			}
			if it.ActualKm != nil {
				p.KmDone += *it.ActualKm
			}
		}
		if it.Status != "upcoming" {
			p.KmPlannedSoFar += it.PlannedKm
		}
	}
	p.KmDone, p.KmPlannedSoFar, p.KmPlannedTotal = round1(p.KmDone), round1(p.KmPlannedSoFar), round1(p.KmPlannedTotal)
	if due > 0 {
		p.AdherencePct = int(math.Round(100 * credit / float64(due)))
	}
	switch {
	case due < 2:
		p.Tone = "good"
	case p.AdherencePct < 50 || p.RecentMissed >= 3:
		p.Tone = "alert"
	case p.AdherencePct < 80 || p.RecentMissed >= 2:
		p.Tone = "watch"
	default:
		p.Tone = "good"
	}
	return p
}

func paceLabel(secPerKm float64) string {
	if secPerKm <= 0 {
		return "—"
	}
	s := int(math.Round(secPerKm))
	return fmt.Sprintf("%d:%02d/km", s/60, s%60)
}

func paceAtFraction(vmaKmh, frac float64) float64 {
	return 3600 / (vmaKmh * frac)
}

// runnerProfileBlock : les mesures propres au coureur — VMA du dernier test, allures
// qui en découlent, zones cardio, âge. Sans elles, le plan et le coach raisonnent
// sur le seul chrono visé, comme pour n'importe qui.
func (h *Handlers) runnerProfileBlock(ctx context.Context, u *models.User, goalKm float64) string {
	var b strings.Builder
	if name := strings.TrimSpace(u.FirstName); name != "" {
		fmt.Fprintf(&b, "- Prénom : %s\n", name)
	}
	if bd, err := time.Parse("2006-01-02", u.BirthDate); err == nil {
		now := time.Now()
		age := now.Year() - bd.Year()
		if now.YearDay() < bd.YearDay() {
			age--
		}
		if age >= 10 && age <= 100 {
			fmt.Fprintf(&b, "- Âge : %d ans\n", age)
		}
	}
	if t, err := h.db.LatestVmaTest(ctx, u.ID); err == nil && t != nil && vma.PlausibleVma(t.VmaKmh) {
		v := t.VmaKmh
		fmt.Fprintf(&b, "- VMA mesurée : %s km/h (test du %s)\n",
			strings.Replace(fmt.Sprintf("%.1f", v), ".", ",", 1), t.CreatedAt.Format("02/01/2006"))
		fmt.Fprintf(&b, "- Allures qui en découlent : endurance fondamentale %s à %s ; seuil autour de %s ; allure VMA %s (400 m en %s)\n",
			paceLabel(paceAtFraction(v, 0.60)), paceLabel(paceAtFraction(v, 0.76)),
			paceLabel(paceAtFraction(v, 0.86)), paceLabel(paceAtFraction(v, 1.0)),
			formatDuration(0.4*paceAtFraction(v, 1.0)))
		if goalKm > 0 {
			target := vma.TargetFor(v, raceDistanceKm(goalKm))
			if target.TimeSec > 0 {
				fmt.Fprintf(&b, "- Potentiel actuel sur la distance de l'objectif : %s (%s)\n",
					formatDuration(target.TimeSec), paceLabel(target.PaceSecPerKm))
			}
		}
	} else {
		b.WriteString("- VMA : pas encore de test dans l'app (tu peux proposer de passer le test de 6 minutes pour caler les allures)\n")
	}
	if ref := h.hrReference(ctx, u); ref != nil && len(ref.Zones) == 5 {
		src := "estimée d'après l'âge"
		if ref.Measured() {
			src = "mesurée au test de VMA"
		}
		fmt.Fprintf(&b, "- FC de référence : %.0f bpm (%s). Zones : Z1 %.0f–%.0f, Z2 %.0f–%.0f (endurance), Z3 %.0f–%.0f, Z4 %.0f–%.0f (seuil), Z5 au-dessus de %.0f\n",
			ref.Bpm, src,
			ref.Zones[0].LowBpm, ref.Zones[0].HighBpm, ref.Zones[1].LowBpm, ref.Zones[1].HighBpm,
			ref.Zones[2].LowBpm, ref.Zones[2].HighBpm, ref.Zones[3].LowBpm, ref.Zones[3].HighBpm, ref.Zones[4].LowBpm)
	}
	if b.Len() == 0 {
		return ""
	}
	return "**Le coureur (mesures de l'app)**\n" + b.String()
}

// raceBlock décrit l'épreuve visée et les courses prévues en chemin.
func raceBlock(g *models.Goal) string {
	var b strings.Builder
	if name := strings.TrimSpace(g.RaceName); name != "" {
		kind := "défi personnel"
		if g.RaceOfficial {
			kind = "course officielle"
		}
		fmt.Fprintf(&b, "- Épreuve visée : %s (%s)\n", name, kind)
	}
	if in := g.RaceInfo; in != nil {
		if in.Place != "" {
			fmt.Fprintf(&b, "- Lieu : %s\n", in.Place)
		}
		if in.ElevationGainM > 0 {
			fmt.Fprintf(&b, "- Dénivelé positif annoncé : %.0f m\n", in.ElevationGainM)
		}
		if in.Course != "" {
			fmt.Fprintf(&b, "- Parcours : %s\n", in.Course)
		}
		if c := in.Climate; c != nil {
			fmt.Fprintf(&b, "- Météo habituelle ce jour-là (moyenne de %d années) : de %.0f à %.0f °C, pluie %.0f %% des années\n",
				c.Years, c.TempMinC, c.TempMaxC, c.RainyDaysPct)
		}
	}
	for _, r := range g.Races {
		line := fmt.Sprintf("- Course prévue en préparation : %s, %s km, le %s",
			r.Name, strings.Replace(fmt.Sprintf("%.1f", r.DistanceKm), ".", ",", 1), r.Date)
		if r.TargetTime != "" {
			line += ", objectif " + r.TargetTime
		}
		if r.Week > 0 {
			line += fmt.Sprintf(" (tient lieu de la séance %d de la semaine %d, id %s)", r.Session, r.Week, r.ID)
		} else {
			line += fmt.Sprintf(" (id %s)", r.ID)
		}
		b.WriteString(line + "\n")
	}
	if b.Len() == 0 {
		return ""
	}
	return "**Courses**\n" + b.String()
}

// planExtras : tout ce qui personnalise la rédaction d'un plan au-delà de la
// distance et du chrono — mesures du coureur, épreuve visée, courses en chemin.
func (h *Handlers) planExtras(ctx context.Context, u *models.User, g *models.Goal) string {
	parts := []string{}
	if p := h.runnerProfileBlock(ctx, u, g.DistanceKm); p != "" {
		parts = append(parts, p+"Cale les allures du plan sur ces mesures quand elles existent : elles priment sur une allure déduite du seul chrono visé.")
	}
	if rb := raceBlock(g); rb != "" {
		parts = append(parts, rb+"Tiens compte du parcours et de la météo habituelle (chaleur : séances d'acclimatation et allures prudentes ; dénivelé : travail de côtes). "+
			"Une course prévue en préparation remplace une séance : allège les deux jours qui la précèdent et prévois une récupération après.")
	}
	if len(g.CoachNotes) > 0 {
		parts = append(parts, "**Ce que le coach a retenu de la personne**\n- "+strings.Join(g.CoachNotes, "\n- "))
	}
	if len(parts) == 0 {
		return ""
	}
	return "\n\n" + strings.Join(parts, "\n\n")
}
