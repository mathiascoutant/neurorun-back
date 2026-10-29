package handlers

import (
	"context"
	"net/http"
	"strings"
	"time"

	"runapp/internal/models"
	"runapp/internal/shoes"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// shoeHabitWindow : les habitudes se lisent sur les quatre derniers mois. Plus
// loin, les paires ont tourné et l'historique dit autre chose.
const shoeHabitWindow = 120 * 24 * time.Hour

// shoeCandidates : les paires actives, telles que la suggestion les lit.
func shoeCandidates(active []shoeView) []shoes.Candidate {
	out := make([]shoes.Candidate, 0, len(active))
	for _, v := range active {
		name := v.Model
		if n := strings.TrimSpace(v.Nickname); n != "" {
			name = n
		}
		out = append(out, shoes.Candidate{ID: v.ID.Hex(), Name: name, Category: v.Category, WearPct: v.Wear.WearPct})
	}
	return out
}

// liveRunProfile : profil d'une course enregistrée, d'après son type, sa note
// et son dénivelé.
func liveRunProfile(run *models.LiveRun) string {
	var scored string
	if run.Score != nil {
		scored = run.Score.SessionType
	}
	var gain float64
	if run.ClientStats != nil {
		gain = run.ClientStats.ElevationGainM
	}
	return shoes.RunProfile(models.NormalizeRunType(run.SessionType), scored, run.DistanceM/1000, gain)
}

// liveRunStart : début d'une course, horodatage GPS sinon enregistrement.
func liveRunStart(run *models.LiveRun) time.Time {
	if run.GpsStartTsMs > 0 {
		return time.UnixMilli(run.GpsStartTsMs)
	}
	return run.CreatedAt
}

// shoeHistory : les courses récentes déjà rattachées à une paire, lues en
// heure de Paris — le footing du mardi soir ne doit pas glisser au mercredi.
func (h *Handlers) shoeHistory(ctx context.Context, userID primitive.ObjectID, now time.Time) []shoes.PastRun {
	runs, err := h.db.ListLiveRunsSince(ctx, userID, now.Add(-shoeHabitWindow))
	if err != nil {
		return nil
	}
	loc, _ := calendarLocation()
	out := make([]shoes.PastRun, 0, len(runs))
	for i := range runs {
		r := &runs[i]
		if r.ShoeID == nil {
			continue
		}
		out = append(out, shoes.PastRun{
			ShoeID:  r.ShoeID.Hex(),
			Profile: liveRunProfile(r),
			Weekday: liveRunStart(r).In(loc).Weekday(),
		})
	}
	return out
}

// guessRunShoe : la paire probablement portée pour cette course, à cocher
// d'avance dans la question de fin de course. Nil faute d'indice.
func (h *Handlers) guessRunShoe(ctx context.Context, userID primitive.ObjectID, run *models.LiveRun, active []shoeView) *shoes.Suggestion {
	loc, _ := calendarLocation()
	history := h.shoeHistory(ctx, userID, time.Now().UTC())
	return shoes.Guess(liveRunProfile(run), liveRunStart(run).In(loc).Weekday(), shoeCandidates(active), history)
}

// SuggestShoe GET /api/shoes/suggest?profile=easy|fast|hilly&date=AAAA-MM-JJ —
// la paire à porter pour une séance à venir, et un conseil de rotation quand
// l'usure le demande. Rien à conseiller sous deux paires actives.
func (h *Handlers) SuggestShoe(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser{}).(*models.User)
	profile := strings.TrimSpace(r.URL.Query().Get("profile"))
	if !shoes.ValidProfile(profile) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "profil invalide (easy, fast ou hilly)"})
		return
	}
	loc, _ := calendarLocation()
	now := time.Now().UTC()
	day := now.In(loc)
	if raw := strings.TrimSpace(r.URL.Query().Get("date")); raw != "" {
		d, err := time.ParseInLocation("2006-01-02", raw, loc)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "date invalide (AAAA-MM-JJ)"})
			return
		}
		day = d
	}

	views, err := h.loadShoeViews(r.Context(), u.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "lecture impossible"})
		return
	}
	active := activeShoes(views)
	empty := map[string]any{"suggestion": nil}
	if len(active) < 2 {
		writeJSON(w, http.StatusOK, empty)
		return
	}
	s := shoes.Advise(profile, day.Weekday(), shoeCandidates(active), h.shoeHistory(r.Context(), u.ID, now))
	if s == nil {
		writeJSON(w, http.StatusOK, empty)
		return
	}
	var pick *shoeView
	for i := range active {
		if active[i].ID.Hex() == s.ShoeID {
			pick = &active[i]
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"suggestion": map[string]any{
			"shoe_id": s.ShoeID,
			"reason":  s.Reason,
			"tip":     s.Tip,
			"shoe":    pick,
		},
	})
}
