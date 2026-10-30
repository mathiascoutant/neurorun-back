package handlers

import (
	"strings"
	"testing"
	"time"

	"runapp/internal/models"
	"runapp/internal/strava"
	"runapp/internal/vma"
)

func TestStravaRunNameUsesParisTime(t *testing.T) {
	// 17 h 30 UTC = 18 h 30 à Paris en octobre (heure d'hiver depuis le 25).
	run := &models.LiveRun{GpsStartTsMs: time.Date(2026, 10, 28, 17, 30, 0, 0, time.UTC).UnixMilli()}
	if got := stravaRunName(run); got != "Course du soir" {
		t.Errorf("nom = %q", got)
	}
	run.Name = "  Sortie longue  "
	if got := stravaRunName(run); got != "Sortie longue" {
		t.Errorf("le nom du coureur doit l'emporter, obtenu %q", got)
	}
}

func TestStravaRunDescription(t *testing.T) {
	run := &models.LiveRun{SessionType: models.RunTypeInterval, Score: &models.RunScore{Result: vma.Result{Total: 82}}}
	got := stravaRunDescription(run)
	for _, want := range []string{"Note NeuroRun : 82/100", "fractionné", "NeuroRun"} {
		if !strings.Contains(got, want) {
			t.Errorf("description %q sans %q", got, want)
		}
	}
}

func TestNearSameRunRecognizesUploadedCopy(t *testing.T) {
	start := time.Date(2026, 10, 28, 7, 30, 0, 0, time.UTC)
	live := appLiveRunToRunActivity(&models.LiveRun{
		GpsStartTsMs: start.UnixMilli(), DistanceM: 10_000, MovingSec: 3000,
		Strava: &models.LiveRunStrava{Status: models.StravaUploadDone, ActivityID: 42},
	})
	// Départ du fichier au premier point GPS, cinq minutes après le bouton.
	copyOnStrava := strava.RunActivity{ID: 42, StartAt: start.Add(5 * time.Minute), DistanceM: 9_500}
	if !nearSameRun(copyOnStrava, live) {
		t.Error("l'activité créée par l'envoi doit être reconnue comme la même course")
	}
	if nearSameRun(strava.RunActivity{ID: 43, StartAt: start.Add(5 * time.Minute), DistanceM: 9_500}, live) {
		t.Error("une autre activité ne doit pas être confondue")
	}
}

func TestStravaTokensCanUpload(t *testing.T) {
	if (&models.StravaTokens{Scope: "read,activity:read_all,profile:read_all"}).CanUpload() {
		t.Error("liaison en lecture seule")
	}
	if !(&models.StravaTokens{Scope: "read,activity:write,activity:read_all"}).CanUpload() {
		t.Error("droit d'écriture accordé")
	}
	var none *models.StravaTokens
	if none.CanUpload() {
		t.Error("sans liaison")
	}
}
