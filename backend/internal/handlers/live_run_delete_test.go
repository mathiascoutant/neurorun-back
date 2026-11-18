package handlers

import (
	"testing"

	"runapp/internal/models"
	"runapp/internal/strava"
)

// La copie Strava d'une course supprimée ne revient pas, les autres sorties restent.
func TestWithoutHiddenStrava(t *testing.T) {
	u := &models.User{HiddenStravaIDs: []int64{42}}
	acts := []strava.RunActivity{{ID: 41}, {ID: 42}, {ID: 43}}
	got := withoutHiddenStrava(u, acts)
	if len(got) != 2 || got[0].ID != 41 || got[1].ID != 43 {
		t.Fatalf("got %+v", got)
	}
	if len(acts) != 3 || acts[1].ID != 42 {
		t.Fatalf("liste d'origine modifiée : %+v", acts)
	}
	if got := withoutHiddenStrava(&models.User{}, acts); len(got) != 3 {
		t.Fatalf("sans masquage : %+v", got)
	}
}
