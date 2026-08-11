package handlers

import (
	"math"
	"time"

	"runapp/internal/models"
	"runapp/internal/strava"
)

// Bornes de plausibilité cardio, alignées sur le front (lib/runMetrics.ts) : un
// capteur qui décroche renvoie des valeurs aberrantes qu'il ne faut pas moyenner.
const (
	hrMinBpm = 30.0
	hrMaxBpm = 235.0
)

// liveRunAvgHR moyenne la FC d'une course NeuroRun.
//
// La fréquence cardiaque n'est pas stockée au niveau de la course : elle vit sur
// chaque point de trace (`hr_bpm`). Sans cette reconstitution, une course
// enregistrée dans NeuroRun n'apporte aucun cardio au tableau de bord, alors même
// que son détail affiche une FC moyenne. Même formule que le front — moyenne
// arithmétique des échantillons plausibles — pour que les deux écrans concordent.
func liveRunAvgHR(lr *models.LiveRun) *float64 {
	var sum float64
	var n int
	for i := range lr.TrackPoints {
		h := lr.TrackPoints[i].HrBpm
		if h == nil || *h < hrMinBpm || *h > hrMaxBpm {
			continue
		}
		sum += *h
		n++
	}
	if n == 0 {
		return nil
	}
	avg := sum / float64(n)
	return &avg
}

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
		AvgHR:      liveRunAvgHR(lr),
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

// enrichFromLiveRun complète une activité Strava avec ce que seule la version
// NeuroRun de la même course possède.
//
// Une sortie enregistrée dans NeuroRun puis retrouvée sur Strava porte sa
// fréquence cardiaque (reconstituée depuis les points de trace), là où la copie
// Strava n'en a pas : l'app web ne pousse pas le flux cardio. Écarter la version
// NeuroRun sans récupérer cette donnée revient à effacer le cardio de la journée.
func enrichFromLiveRun(dst *strava.RunActivity, live strava.RunActivity) {
	if dst.AvgHR == nil && live.AvgHR != nil {
		dst.AvgHR = live.AvgHR
	}
}

// mergeStravaAndLiveRuns ajoute les courses live qui ne sont vraisemblablement pas déjà présentes côté Strava.
func mergeStravaAndLiveRuns(stravaRuns []strava.RunActivity, liveRuns []strava.RunActivity) []strava.RunActivity {
	out := make([]strava.RunActivity, 0, len(stravaRuns)+len(liveRuns))
	out = append(out, stravaRuns...)
	// Les doublons se cherchent uniquement parmi les sorties Strava : deux
	// courses NeuroRun rapprochées restent deux courses distinctes.
	stravaCount := len(out)
next:
	for _, l := range liveRuns {
		for i := 0; i < stravaCount; i++ {
			if nearSameRun(out[i], l) {
				enrichFromLiveRun(&out[i], l)
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
