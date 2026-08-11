package handlers

import (
	"math"
	"testing"
	"time"

	"runapp/internal/models"
	"runapp/internal/strava"
)

func hrPtr(v float64) *float64 { return &v }

// La FC d'une course NeuroRun vit sur les points de trace : si elle n'est pas
// reconstituée ici, le tableau de bord affiche une courbe cardio vide alors que
// le détail de la course, lui, montre bien une moyenne.
func TestLiveRunAvgHRFromTrackPoints(t *testing.T) {
	lr := &models.LiveRun{
		TrackPoints: []models.LiveRunTrackPoint{
			{HrBpm: hrPtr(180)},
			{HrBpm: hrPtr(190)},
			{HrBpm: nil},        // capteur muet : ignoré, pas compté comme 0
			{HrBpm: hrPtr(500)}, // aberration : hors bornes de plausibilité
			{HrBpm: hrPtr(2)},   // idem
		},
	}
	got := liveRunAvgHR(lr)
	if got == nil {
		t.Fatal("FC moyenne attendue, obtenu nil")
	}
	if *got != 185 {
		t.Fatalf("FC moyenne attendue 185, obtenu %v", *got)
	}
}

func TestLiveRunAvgHRWithoutSensor(t *testing.T) {
	lr := &models.LiveRun{
		TrackPoints: []models.LiveRunTrackPoint{{HrBpm: nil}, {HrBpm: nil}},
	}
	if got := liveRunAvgHR(lr); got != nil {
		t.Fatalf("sans capteur, nil attendu (et non 0), obtenu %v", *got)
	}
}

// Une course enregistrée dans NeuroRun puis synchronisée vers Strava est écartée
// comme doublon — mais elle est la seule à porter la fréquence cardiaque, que
// Strava n'a pas. Sans récupération, le cardio de la journée disparaît.
//
// Cas réel : deux sorties le 11/08, une de 4312 m à 184,7 bpm doublée sur Strava
// et une de 150 m à 137 bpm. Le tableau de bord affichait 137 — la petite course
// seule — au lieu de la moyenne pondérée des deux.
func TestDedupKeepsHeartRateFromLiveRun(t *testing.T) {
	start := time.Date(2026, 8, 11, 9, 22, 49, 0, time.UTC)
	hrBig, hrSmall := 184.7, 137.0

	stravaRuns := []strava.RunActivity{
		{StartAt: start, DistanceM: 4312, MovingSec: 1310, AvgSpeed: 3.29, AvgHR: nil},
	}
	liveRuns := []strava.RunActivity{
		{StartAt: start, DistanceM: 4312, MovingSec: 1310, AvgSpeed: 3.29, AvgHR: &hrBig},
		{StartAt: start.Add(-25 * time.Minute), DistanceM: 150, MovingSec: 51, AvgSpeed: 2.94, AvgHR: &hrSmall},
	}

	merged := mergeStravaAndLiveRuns(stravaRuns, liveRuns)
	if len(merged) != 2 {
		t.Fatalf("le doublon doit rester dédoublonné : 2 sorties attendues, obtenu %d", len(merged))
	}
	if merged[0].AvgHR == nil {
		t.Fatal("la FC de la version NeuroRun doit être reportée sur la version Strava")
	}
	if *merged[0].AvgHR != hrBig {
		t.Fatalf("FC attendue %v, obtenu %v", hrBig, *merged[0].AvgHR)
	}

	day := strava.BuildDashboard(merged, "7d").Daily[0]
	want := (hrBig*1310 + hrSmall*51) / (1310 + 51)
	if day.AvgHR == nil || math.Abs(*day.AvgHR-want) > 0.1 {
		t.Fatalf("FC du jour attendue ≈ %.1f, obtenu %v", want, day.AvgHR)
	}
}

// Une FC déjà fournie par Strava fait foi : on ne l'écrase pas.
func TestDedupKeepsStravaHeartRateWhenPresent(t *testing.T) {
	start := time.Date(2026, 8, 11, 9, 22, 49, 0, time.UTC)
	hrStrava, hrLive := 176.0, 184.7

	merged := mergeStravaAndLiveRuns(
		[]strava.RunActivity{{StartAt: start, DistanceM: 4312, MovingSec: 1310, AvgHR: &hrStrava}},
		[]strava.RunActivity{{StartAt: start, DistanceM: 4312, MovingSec: 1310, AvgHR: &hrLive}},
	)
	if *merged[0].AvgHR != hrStrava {
		t.Fatalf("la FC Strava doit primer : attendu %v, obtenu %v", hrStrava, *merged[0].AvgHR)
	}
}

// La conversion complète doit propager la FC jusqu'à l'activité agrégée.
func TestAppLiveRunCarriesHeartRate(t *testing.T) {
	lr := &models.LiveRun{
		CreatedAt:   time.Now().UTC(),
		DistanceM:   4310,
		MovingSec:   1310,
		WallSec:     1350,
		TrackPoints: []models.LiveRunTrackPoint{{HrBpm: hrPtr(184)}, {HrBpm: hrPtr(186)}},
	}
	act := appLiveRunToRunActivity(lr)
	if act.AvgHR == nil {
		t.Fatal("AvgHR ne doit plus être nil pour une course avec capteur")
	}
	if *act.AvgHR != 185 {
		t.Fatalf("AvgHR attendu 185, obtenu %v", *act.AvgHR)
	}
}
