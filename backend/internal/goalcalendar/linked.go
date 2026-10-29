package goalcalendar

import (
	"time"

	"runapp/internal/models"
	"runapp/internal/strava"
)

// LinkWindowDays : une séance lancée depuis l'objectif peut se courir la veille
// ou le lendemain de son jour. Au-delà, ce n'est plus la même semaine de plan,
// c'est un report à demander au coach.
const LinkWindowDays = 1

// sameRunTolerance : écart de départ sous lequel une sortie Strava est la copie
// d'une course NeuroRun (montre → Santé → Strava), et non une autre sortie.
const sameRunTolerance = 15 * time.Minute

// LinkedRun est une course NeuroRun lancée depuis une séance du plan. Elle compte
// pour cette séance-là, quel que soit le jour du calendrier où elle tombe.
type LinkedRun struct {
	Week      int
	Session   int
	LiveRunID string
	Run       strava.RunActivity
}

// LiveRunStart : instant de départ d'une course NeuroRun. La date d'envoi
// ne vaut qu'à défaut — une course de montre peut arriver des heures après.
func LiveRunStart(lr models.LiveRun) time.Time {
	if lr.GpsStartTsMs > 0 {
		return time.UnixMilli(lr.GpsStartTsMs).UTC()
	}
	return lr.CreatedAt
}

// LinkedRunFromLive lit une course rattachée à une séance ; faux si elle ne l'est pas.
func LinkedRunFromLive(lr models.LiveRun) (LinkedRun, bool) {
	if lr.GoalSession == nil {
		return LinkedRun{}, false
	}
	run := strava.RunActivity{
		Name:      lr.Name,
		Type:      "Run",
		StartAt:   LiveRunStart(lr),
		DistanceM: lr.DistanceM,
		MovingSec: int(lr.MovingSec),
	}
	// Le type choisi au départ fait foi : pas besoin de deviner le fractionné
	// dans le nom de la course.
	if lr.SessionType == models.RunTypeInterval {
		run.WorkoutType = strava.WorkoutTypeRunWorkout
	}
	return LinkedRun{
		Week:      lr.GoalSession.Week,
		Session:   lr.GoalSession.Session,
		LiveRunID: lr.ID.Hex(),
		Run:       run,
	}, true
}

// FindScheduled renvoie la séance (semaine, numéro) du planning résolu.
func FindScheduled(sessions []ScheduledSession, week, session int) (ScheduledSession, bool) {
	for _, s := range sessions {
		if s.Week == week && s.Session == session {
			return s, true
		}
	}
	return ScheduledSession{}, false
}

// civilDayDiff compte les jours civils de b à a, sans se laisser piéger par
// les changements d'heure (un jour peut durer 23 ou 25 h).
func civilDayDiff(a, b time.Time, loc *time.Location) int {
	ay, am, ad := a.In(loc).Date()
	by, bm, bd := b.In(loc).Date()
	da := time.Date(ay, am, ad, 12, 0, 0, 0, time.UTC)
	db := time.Date(by, bm, bd, 12, 0, 0, 0, time.UTC)
	return int(da.Sub(db).Hours() / 24)
}

// CanLinkRun dit si une course partie à `start` peut compter pour la séance :
// séance maintenue, et courue au plus LinkWindowDays jour(s) de son jour.
func CanLinkRun(s ScheduledSession, start time.Time, loc *time.Location) bool {
	if s.Skipped || s.Date.IsZero() || start.IsZero() {
		return false
	}
	if loc == nil {
		loc = time.UTC
	}
	d := civilDayDiff(start, s.Date, loc)
	return d >= -LinkWindowDays && d <= LinkWindowDays
}

// withoutLinkedCopies retire des sorties Strava celles qui sont la copie d'une
// course rattachée : sinon la même course, faite la veille d'un fractionné,
// validerait aussi la séance du jour où elle a été courue.
func withoutLinkedCopies(runs []strava.RunActivity, linked []LinkedRun) []strava.RunActivity {
	if len(linked) == 0 {
		return runs
	}
	out := make([]strava.RunActivity, 0, len(runs))
	for _, r := range runs {
		copyOf := false
		for _, l := range linked {
			d := r.StartAt.Sub(l.Run.StartAt)
			if d < 0 {
				d = -d
			}
			if d <= sameRunTolerance {
				copyOf = true
				break
			}
		}
		if !copyOf {
			out = append(out, r)
		}
	}
	return out
}

// bestLinkedRun choisit, parmi les courses rattachées à une séance, celle qui
// la remplit le mieux (mêmes règles qu'une sortie du jour). Les identifiants
// négatifs ne servent qu'à retrouver la course : ils ne sont jamais exposés.
func bestLinkedRun(list []LinkedRun, minKm float64, paceTarget *float64) *LinkedRun {
	if len(list) == 0 {
		return nil
	}
	runs := make([]strava.RunActivity, len(list))
	for i, l := range list {
		runs[i] = l.Run
		runs[i].ID = -int64(i + 1)
	}
	best := BestRunForSession(runs, minKm, paceTarget, nil)
	if best == nil {
		return nil
	}
	return &list[-best.ID-1]
}
