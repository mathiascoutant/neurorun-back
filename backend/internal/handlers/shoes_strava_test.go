package handlers

import (
	"testing"
	"time"

	"runapp/internal/models"
	"runapp/internal/strava"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

func TestStravaRunFromActivityFlagsNeuroRunCopy(t *testing.T) {
	user := primitive.NewObjectID()
	start := time.Date(2026, 10, 28, 7, 30, 0, 0, time.UTC)
	live := []strava.RunActivity{{StartAt: start.Add(40 * time.Second), DistanceM: 10_050}}

	copyOfLive := stravaRunFromActivity(user, strava.RunActivity{ID: 1, StartAt: start, DistanceM: 10_120, MovingSec: 3000}, live)
	if !copyOfLive.Duplicate {
		t.Error("la copie Strava d'une course NeuroRun ne doit être ni demandée ni comptée")
	}
	other := stravaRunFromActivity(user, strava.RunActivity{ID: 2, StartAt: start.Add(9 * time.Hour), DistanceM: 6000, WorkoutType: 3}, live)
	if other.Duplicate {
		t.Error("une sortie Strava distincte doit être demandée")
	}
	if !other.Interval || other.StravaID != 2 || other.UserID != user {
		t.Errorf("sortie mal reprise : %+v", other)
	}
}

func TestTakeStravaShoeSlotThrottles(t *testing.T) {
	user := primitive.NewObjectID()
	now := time.Now()
	if !takeStravaShoeSlot(user, now) {
		t.Fatal("première relecture refusée")
	}
	if takeStravaShoeSlot(user, now.Add(time.Minute)) {
		t.Error("une relecture une minute après doit attendre")
	}
	if !takeStravaShoeSlot(user, now.Add(stravaShoeSyncEvery+time.Second)) {
		t.Error("la relecture doit repartir après le délai")
	}
}

func TestStravaRunAsLiveKeepsWhatTheRecapNeeds(t *testing.T) {
	r := &models.StravaRun{
		ID: primitive.NewObjectID(), StartedAt: time.Date(2026, 10, 28, 18, 0, 0, 0, time.UTC),
		Name: "Sortie du soir", DistanceM: 8000, MovingSec: 2400, ElevGainM: 120, Interval: true,
	}
	lr := stravaRunAsLive(r)
	if lr.ID != r.ID || lr.SessionType != models.RunTypeInterval || lr.ClientStats == nil || lr.ClientStats.ElevationGainM != 120 {
		t.Errorf("récap incomplet : %+v", lr)
	}
	if !liveRunStart(lr).Equal(r.StartedAt) {
		t.Errorf("départ = %v, attendu %v", liveRunStart(lr), r.StartedAt)
	}
}
