package handlers

import (
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"time"

	"runapp/internal/models"
	"runapp/internal/store"

	"github.com/go-chi/chi/v5"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

const (
	maxLiveRunTrackPoints = 3500
	maxLiveRunSplits      = 250
)

type liveRunCreateBody struct {
	TargetKm   float64 `json:"target_km"`
	DistanceM  float64 `json:"distance_m"`
	MovingSec  float64 `json:"moving_sec"`
	WallSec    float64 `json:"wall_sec"`
	GpsStartTsMs int64 `json:"gps_start_ts_ms"`
	GpsEndTsMs   int64 `json:"gps_end_ts_ms"`

	AvgPaceSecPerKm    float64 `json:"avg_pace_sec_per_km"`
	MaxImpliedSpeedKmh float64 `json:"max_implied_speed_kmh"`
	Splits             []models.LiveRunSplit `json:"splits"`
	TrackPoints        []models.LiveRunTrackPoint `json:"track_points"`

	ClientVersion     string `json:"client_version"`
	UserAgent         string `json:"user_agent"`
	NavigatorLanguage string `json:"navigator_language"`
	ScreenW           int    `json:"screen_w"`
	ScreenH           int    `json:"screen_h"`
	OnlineAtEnd       bool   `json:"online_at_end"`
	AutoPauseDetected bool   `json:"auto_pause_detected"`
}

func (h *Handlers) CreateLiveRun(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser{}).(*models.User)
	var b liveRunCreateBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "json invalide"})
		return
	}

	if b.DistanceM < 0 || b.DistanceM > 1e7 || math.IsNaN(b.DistanceM) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "distance_m invalide"})
		return
	}
	if b.MovingSec < 0 || b.WallSec < 0 || b.MovingSec > 86400*3 || b.WallSec > 86400*3 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "durées invalides"})
		return
	}
	if len(b.Splits) > maxLiveRunSplits {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "trop de splits"})
		return
	}
	if len(b.TrackPoints) > maxLiveRunTrackPoints {
		b.TrackPoints = b.TrackPoints[:maxLiveRunTrackPoints]
	}

	avg := b.AvgPaceSecPerKm
	if (avg <= 0 || math.IsNaN(avg)) && b.DistanceM > 5 && b.MovingSec > 0 {
		avg = b.MovingSec / (b.DistanceM / 1000)
	}

	run := models.LiveRun{
		UserID:             u.ID,
		TargetKm:           b.TargetKm,
		DistanceM:          b.DistanceM,
		MovingSec:          b.MovingSec,
		WallSec:            b.WallSec,
		GpsStartTsMs:       b.GpsStartTsMs,
		GpsEndTsMs:         b.GpsEndTsMs,
		AvgPaceSecPerKm:    avg,
		MaxImpliedSpeedKmh: b.MaxImpliedSpeedKmh,
		Splits:             b.Splits,
		TrackPoints:        b.TrackPoints,
		ClientVersion:      b.ClientVersion,
		UserAgent:          b.UserAgent,
		NavigatorLanguage:  b.NavigatorLanguage,
		ScreenW:            b.ScreenW,
		ScreenH:            b.ScreenH,
		OnlineAtEnd:        b.OnlineAtEnd,
		AutoPauseDetected:  b.AutoPauseDetected,
	}

	if err := h.db.CreateLiveRun(r.Context(), &run); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "enregistrement impossible"})
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"id":         run.ID.Hex(),
		"created_at": run.CreatedAt.Format(time.RFC3339),
	})
}

func (h *Handlers) ListLiveRuns(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser{}).(*models.User)
	list, err := h.db.ListLiveRunsByUser(r.Context(), u.ID, 80)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "liste impossible"})
		return
	}
	out := make([]models.LiveRunListItem, 0, len(list))
	for _, lr := range list {
		out = append(out, models.LiveRunListItem{
			ID:              lr.ID.Hex(),
			CreatedAt:       lr.CreatedAt.UTC().Format(time.RFC3339),
			TargetKm:        lr.TargetKm,
			DistanceM:       lr.DistanceM,
			MovingSec:       lr.MovingSec,
			WallSec:         lr.WallSec,
			AvgPaceSecPerKm: lr.AvgPaceSecPerKm,
			SplitCount:      len(lr.Splits),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": out})
}

func (h *Handlers) GetLiveRun(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser{}).(*models.User)
	idStr := chi.URLParam(r, "id")
	oid, err := primitive.ObjectIDFromHex(idStr)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id invalide"})
		return
	}
	run, err := h.db.GetLiveRunByUser(r.Context(), u.ID, oid)
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "introuvable"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "lecture impossible"})
		return
	}
	writeJSON(w, http.StatusOK, liveRunToJSON(run))
}

func liveRunToJSON(lr *models.LiveRun) map[string]any {
	return map[string]any{
		"id":                    lr.ID.Hex(),
		"created_at":            lr.CreatedAt.UTC().Format(time.RFC3339),
		"target_km":             lr.TargetKm,
		"distance_m":            lr.DistanceM,
		"moving_sec":            lr.MovingSec,
		"wall_sec":              lr.WallSec,
		"gps_start_ts_ms":       lr.GpsStartTsMs,
		"gps_end_ts_ms":         lr.GpsEndTsMs,
		"avg_pace_sec_per_km":   lr.AvgPaceSecPerKm,
		"max_implied_speed_kmh": lr.MaxImpliedSpeedKmh,
		"splits":                lr.Splits,
		"track_points":          lr.TrackPoints,
		"client_version":        lr.ClientVersion,
		"user_agent":            lr.UserAgent,
		"navigator_language":    lr.NavigatorLanguage,
		"screen_w":              lr.ScreenW,
		"screen_h":              lr.ScreenH,
		"online_at_end":         lr.OnlineAtEnd,
		"auto_pause_detected":   lr.AutoPauseDetected,
	}
}
