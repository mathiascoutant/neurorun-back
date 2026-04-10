package handlers

import (
	"math"
	"time"

	"runapp/internal/models"
	"runapp/internal/strava"
)

// appLiveRunToRunActivity convertit une course enregistrée via POST /api/live-runs pour agrégation dashboard.
func appLiveRunToRunActivity(lr *models.LiveRun) strava.RunActivity {
	mov := int(lr.MovingSec)
	if mov < 0 {
		mov = 0
	}
	dist := lr.DistanceM
	var avgSp float64
	if mov > 0 && dist > 0 {
		avgSp = dist / float64(mov)
	} else if lr.AvgPaceSecPerKm > 0 {
		avgSp = 1000.0 / lr.AvgPaceSecPerKm
	}
	startAt := lr.CreatedAt.UTC()
	if lr.GpsStartTsMs > 0 {
		startAt = time.UnixMilli(lr.GpsStartTsMs).UTC()
	}
	wall := int(lr.WallSec)
	if wall < mov {
		wall = mov
	}
	return strava.RunActivity{
		ID:         0,
		Name:       "Course NeuroRun",
		Type:       "Run",
		StartAt:    startAt,
		DistanceM:  dist,
		MovingSec:  mov,
		ElapsedSec: wall,
		AvgSpeed:   avgSp,
		AvgHR:      nil,
	}
}

func liveRunsToRunActivities(live []models.LiveRun, after *int64) []strava.RunActivity {
	var out []strava.RunActivity
	for i := range live {
		lr := &live[i]
		startAt := lr.CreatedAt.UTC()
		if lr.GpsStartTsMs > 0 {
			t := time.UnixMilli(lr.GpsStartTsMs).UTC()
			if t.Before(startAt) {
				startAt = t
			}
		}
		if after != nil && startAt.Unix() < *after {
			continue
		}
		out = append(out, appLiveRunToRunActivity(lr))
	}
	return out
}

// mergeStravaAndLiveRuns ajoute les courses live qui ne sont vraisemblablement pas déjà présentes côté Strava.
func mergeStravaAndLiveRuns(stravaRuns []strava.RunActivity, liveRuns []strava.RunActivity) []strava.RunActivity {
	out := make([]strava.RunActivity, 0, len(stravaRuns)+len(liveRuns))
	out = append(out, stravaRuns...)
next:
	for _, l := range liveRuns {
		for _, s := range stravaRuns {
			if nearSameRun(s, l) {
				continue next
			}
		}
		out = append(out, l)
	}
	return out
}

func nearSameRun(a, b strava.RunActivity) bool {
	sec := math.Abs(a.StartAt.Sub(b.StartAt).Seconds())
	if sec > 180 {
		return false
	}
	dm := math.Abs(a.DistanceM - b.DistanceM)
	return dm < 400
}
