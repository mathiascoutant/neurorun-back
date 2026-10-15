package handlers

import (
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"sort"
	"strings"
	"unicode/utf8"

	"runapp/internal/models"
	"runapp/internal/store"
	"runapp/internal/strava"
	"runapp/internal/vma"

	"github.com/go-chi/chi/v5"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// Vitesse instantanée retenue (m/s) : au-delà, c'est un saut GPS.
const maxTrackSpeedMps = 12.0

// liveRunIntervals découpe un fractionné en répétitions et récupérations, à
// partir de la vitesse le long de la trace ; à défaut, sur les kilomètres
// (résumé sans détail bloc par bloc).
func liveRunIntervals(run *models.LiveRun) *strava.IntervalSummary {
	pts := make([]models.LiveRunTrackPoint, 0, len(run.TrackPoints))
	for _, p := range run.TrackPoints {
		if p.TMs > 0 {
			pts = append(pts, p)
		}
	}
	sort.Slice(pts, func(i, j int) bool { return pts[i].TMs < pts[j].TMs })

	if len(pts) >= 60 {
		t0 := pts[0].TMs
		times := make([]int, len(pts))
		vel := make([]float64, len(pts))
		hr := make([]float64, len(pts))
		for i, p := range pts {
			times[i] = int(math.Round(float64(p.TMs-t0) / 1000))
			switch {
			case p.SpeedMps != nil && *p.SpeedMps >= 0:
				vel[i] = *p.SpeedMps
			case i > 0:
				// Sans vitesse mesurée : celle qu'impose le déplacement depuis le
				// point précédent.
				if dt := float64(p.TMs-pts[i-1].TMs) / 1000; dt > 0 {
					vel[i] = haversineM(pts[i-1].Lat, pts[i-1].Lng, p.Lat, p.Lng) / dt
				}
			}
			vel[i] = math.Min(math.Max(vel[i], 0), maxTrackSpeedMps)
			if p.HrBpm != nil {
				hr[i] = *p.HrBpm
			}
		}
		if s := strava.IntervalSummaryFromTrack(times, vel, hr); s != nil {
			return s
		}
	}
	return strava.IntervalSummaryFromSplits(run.Splits)
}

// intervalInput : les répétitions d'un découpage, telles que la note les lit.
// Sans détail bloc par bloc (découpage sur les kilomètres), pas de répétitions :
// la note juge alors la sortie comme une course continue.
func intervalInput(s *strava.IntervalSummary) *vma.IntervalInput {
	if s == nil || len(s.Segments) == 0 {
		return nil
	}
	in := &vma.IntervalInput{}
	for i, seg := range s.Segments {
		if seg.Kind != strava.SegmentWork {
			continue
		}
		in.Reps = append(in.Reps, vma.IntervalSegment{
			DistanceM: seg.DistanceM,
			Sec:       seg.Sec,
			HrBpm:     seg.AvgHeartrate,
		})
		rec := 0.0
		if i+1 < len(s.Segments) && s.Segments[i+1].Kind == strava.SegmentRecovery {
			rec = s.Segments[i+1].Sec
		}
		in.RecoveryAfter = append(in.RecoveryAfter, rec)
	}
	return in
}

func haversineM(lat1, lng1, lat2, lng2 float64) float64 {
	const r = 6371000.0
	toRad := math.Pi / 180
	dLat := (lat2 - lat1) * toRad
	dLng := (lng2 - lng1) * toRad
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1*toRad)*math.Cos(lat2*toRad)*math.Sin(dLng/2)*math.Sin(dLng/2)
	return 2 * r * math.Asin(math.Min(1, math.Sqrt(a)))
}

type renameLiveRunBody struct {
	Name string `json:"name"`
}

// RenameLiveRun PATCH /api/live-runs/{id} {name} — renomme sa course. Un nom
// vide rend le nom par défaut (« Course du soir »…).
func (h *Handlers) RenameLiveRun(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser{}).(*models.User)
	if !h.requireCapability(w, r, u, "live_runs") {
		return
	}
	runID, err := primitive.ObjectIDFromHex(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id invalide"})
		return
	}
	var b renameLiveRunBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "corps invalide"})
		return
	}
	name, ok := cleanRunName(b.Name)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "nom trop long"})
		return
	}
	if err := h.db.SetLiveRunName(r.Context(), u.ID, runID, name); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "course introuvable"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "enregistrement impossible"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"name": name})
}

// cleanRunName : espaces superflus retirés, retours à la ligne aplatis. Faux
// si le nom dépasse la longueur permise.
func cleanRunName(raw string) (string, bool) {
	name := strings.Join(strings.Fields(raw), " ")
	return name, utf8.RuneCountInString(name) <= models.MaxRunNameLen
}
