package handlers

import (
	"errors"
	"log"
	"net/http"

	"runapp/internal/models"
	"runapp/internal/store"
	"runapp/internal/strava"

	"github.com/go-chi/chi/v5"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// DeleteLiveRun DELETE /api/live-runs/{id} — supprime une course NeuroRun du coureur.
//
// Strava n'offre aucun moyen à une appli d'effacer une activité : sa copie est
// masquée partout dans NeuroRun, et l'app reçoit son identifiant pour ouvrir Strava
// où le coureur la supprime d'un geste. Pas de contrôle d'offre : on peut toujours
// effacer ses propres données.
func (h *Handlers) DeleteLiveRun(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser{}).(*models.User)
	runID, err := primitive.ObjectIDFromHex(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id invalide"})
		return
	}
	run, err := h.db.GetLiveRunMetaByID(r.Context(), runID)
	if err != nil || run.UserID != u.ID {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "course introuvable"})
		return
	}
	// Une copie en cours d'envoi est masquée par pushRunToStrava à son arrivée.
	var stravaID int64
	if run.Strava != nil && run.Strava.Status == models.StravaUploadDone {
		stravaID = run.Strava.ActivityID
	}
	if stravaID != 0 {
		// Avant la suppression : si elle échoue ensuite, la course reste visible et
		// le coureur réessaie ; l'inverse ferait réapparaître la copie Strava.
		if err := h.db.HideStravaActivity(r.Context(), u.ID, stravaID); err != nil {
			log.Printf("suppression: masquage Strava %d: %v", stravaID, err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "suppression impossible"})
			return
		}
	}
	if err := h.db.DeleteLiveRun(r.Context(), u.ID, runID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "course introuvable"})
			return
		}
		log.Printf("suppression: course %s: %v", runID.Hex(), err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "suppression impossible"})
		return
	}
	out := map[string]any{"deleted": true}
	if stravaID != 0 {
		out["strava_activity_id"] = stravaID
	}
	writeJSON(w, http.StatusOK, out)
}

// withoutHiddenStrava retire les copies Strava de courses supprimées. À passer sur
// toute liste lue chez Strava, sans quoi la course effacée y revient sous sa forme
// Strava.
func withoutHiddenStrava(u *models.User, acts []strava.RunActivity) []strava.RunActivity {
	if len(u.HiddenStravaIDs) == 0 {
		return acts
	}
	out := acts[:0:0]
	for _, a := range acts {
		if !u.HidesStrava(a.ID) {
			out = append(out, a)
		}
	}
	return out
}
