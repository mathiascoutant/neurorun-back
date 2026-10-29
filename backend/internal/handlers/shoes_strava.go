package handlers

import (
	"context"
	"log"
	"sync"
	"time"

	"runapp/internal/models"
	"runapp/internal/strava"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// stravaShoeSyncEvery : une relecture de Strava au plus par coureur et par
// période. La question des chaussures est posée à chaque retour dans l'app ;
// Strava, lui, limite le nombre d'appels pour toute l'application.
const stravaShoeSyncEvery = 10 * time.Minute

var stravaShoeSync = struct {
	sync.Mutex
	last map[primitive.ObjectID]time.Time
}{last: map[primitive.ObjectID]time.Time{}}

// takeStravaShoeSlot : vrai si ce coureur peut relire Strava maintenant, et
// réserve le créneau — un échec compte aussi, pour ne pas insister.
func takeStravaShoeSlot(userID primitive.ObjectID, now time.Time) bool {
	stravaShoeSync.Lock()
	defer stravaShoeSync.Unlock()
	if last, ok := stravaShoeSync.last[userID]; ok && now.Sub(last) < stravaShoeSyncEvery {
		return false
	}
	stravaShoeSync.last[userID] = now
	return true
}

// stravaRunFromActivity : ce que le suivi des chaussures garde d'une sortie
// Strava. `duplicate` : copie d'une course NeuroRun, qui porte déjà la paire.
func stravaRunFromActivity(userID primitive.ObjectID, a strava.RunActivity, live []strava.RunActivity) models.StravaRun {
	dup := false
	for _, l := range live {
		if nearSameRun(a, l) {
			dup = true
			break
		}
	}
	return models.StravaRun{
		UserID:    userID,
		StravaID:  a.ID,
		StartedAt: a.StartAt.UTC(),
		Name:      a.Name,
		DistanceM: a.DistanceM,
		MovingSec: a.MovingSec,
		ElevGainM: a.ElevGainM,
		Interval:  a.WorkoutType == 3,
		Duplicate: dup,
	}
}

// syncStravaRunsForShoes relit les sorties Strava récentes pour qu'elles entrent
// dans la question « avec quelle paire ? » comme une course NeuroRun. Sans
// Strava lié, ou relu il y a peu, rien à faire. Un échec n'empêche jamais la
// question de porter sur les courses NeuroRun.
func (h *Handlers) syncStravaRunsForShoes(ctx context.Context, u *models.User, since time.Time) {
	if !u.HasStrava() || !takeStravaShoeSlot(u.ID, time.Now()) {
		return
	}
	access, err := h.ensureStravaAccess(ctx, u)
	if err != nil {
		h.forgetStravaIfRevoked(ctx, u, err)
		return
	}
	after := since.Unix()
	acts, err := h.strava.FetchRunActivities(ctx, access, &after)
	if err != nil {
		h.forgetStravaIfRevoked(ctx, u, err)
		log.Printf("chaussures: sorties Strava: %v", err)
		return
	}
	if len(acts) == 0 {
		return
	}
	// Une course NeuroRun peut démarrer un peu avant `since` et finir après : on
	// élargit la fenêtre pour reconnaître sa copie Strava.
	liveRuns, err := h.db.ListLiveRunsSince(ctx, u.ID, since.Add(-time.Hour))
	if err != nil {
		return
	}
	live := liveRunsToRunActivities(liveRuns, nil)
	for _, a := range acts {
		r := stravaRunFromActivity(u.ID, a, live)
		if err := h.db.UpsertStravaRun(ctx, &r); err != nil {
			log.Printf("chaussures: sortie Strava %d: %v", a.ID, err)
		}
	}
}

// stravaRunAsLive : la sortie Strava sous la forme qu'attendent la suggestion
// de paire et le récapitulatif.
func stravaRunAsLive(r *models.StravaRun) *models.LiveRun {
	lr := &models.LiveRun{
		ID:           r.ID,
		CreatedAt:    r.StartedAt,
		GpsStartTsMs: r.StartedAt.UnixMilli(),
		Name:         r.Name,
		DistanceM:    r.DistanceM,
		MovingSec:    float64(r.MovingSec),
		SessionType:  models.RunTypeFree,
	}
	if r.Interval {
		lr.SessionType = models.RunTypeInterval
	}
	if r.ElevGainM > 0 {
		lr.ClientStats = &models.LiveRunClientStats{ElevationGainM: r.ElevGainM}
	}
	return lr
}
