package handlers

import (
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"runapp/internal/models"
	"runapp/internal/store"
	"runapp/internal/strava"

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
	ClientStats        *models.LiveRunClientStats `json:"client_stats"`
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
	if !h.requireCapability(w, r, u, "live_runs") {
		return
	}
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
		ClientStats:        b.ClientStats,
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
	if !h.requireCapability(w, r, u, "live_runs") {
		return
	}
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

type runHistoryFeedRow struct {
	at time.Time
	m  map[string]any
}

// RunHistoryFeed renvoie une page d’historique mélangé : courses NeuroRun + courses Strava si le compte est lié.
// Query: limit (défaut 10, max 30), before (RFC3339 / RFC3339Nano, curseur exclusif pour la pagination).
func (h *Handlers) RunHistoryFeed(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser{}).(*models.User)
	if !h.requireCapability(w, r, u, "live_runs") {
		return
	}

	limit := 10
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 30 {
			limit = n
		}
	}

	cursorBefore := time.Now().UTC().Add(time.Second)
	if b := strings.TrimSpace(r.URL.Query().Get("before")); b != "" {
		var parsed time.Time
		var err error
		parsed, err = time.Parse(time.RFC3339Nano, b)
		if err != nil {
			parsed, err = time.Parse(time.RFC3339, b)
		}
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "before invalide (RFC3339)"})
			return
		}
		cursorBefore = parsed.UTC()
	}

	fetchN := limit * 5
	if fetchN > 80 {
		fetchN = 80
	}
	if fetchN < 20 {
		fetchN = 20
	}

	liveList, err := h.db.ListLiveRunsByUserBefore(r.Context(), u.ID, cursorBefore, fetchN)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "liste live impossible"})
		return
	}

	var stravaActs []strava.RunActivity
	stravaIncluded := false
	if u.HasStrava() {
		access, err := h.ensureStravaAccess(r.Context(), u)
		if err == nil {
			stravaActs, err = h.strava.FetchRunActivitiesBefore(r.Context(), access, cursorBefore, fetchN)
			if err == nil {
				stravaIncluded = true
			}
		}
	}

	var rows []runHistoryFeedRow
	for _, lr := range liveList {
		at := lr.CreatedAt.UTC()
		rows = append(rows, runHistoryFeedRow{
			at: at,
			m: map[string]any{
				"source":              "live",
				"id":                  lr.ID.Hex(),
				"created_at":          at.Format(time.RFC3339),
				"distance_m":          lr.DistanceM,
				"moving_sec":          lr.MovingSec,
				"avg_pace_sec_per_km": lr.AvgPaceSecPerKm,
				"split_count":         len(lr.Splits),
			},
		})
	}
	for _, ar := range stravaActs {
		at := ar.StartAt.UTC()
		pace := 0.0
		if ar.AvgSpeed > 0 {
			pace = 1000.0 / ar.AvgSpeed
		}
		row := map[string]any{
			"source":              "strava",
			"strava_activity_id":  ar.ID,
			"name":                ar.Name,
			"start_date":          at.Format(time.RFC3339),
			"distance_m":          ar.DistanceM,
			"moving_sec":          float64(ar.MovingSec),
			"avg_pace_sec_per_km": pace,
			"activity_type":       ar.Type,
		}
		rows = append(rows, runHistoryFeedRow{at: at, m: row})
	}

	sort.Slice(rows, func(i, j int) bool {
		return rows[i].at.After(rows[j].at)
	})

	if len(rows) > limit {
		rows = rows[:limit]
	}

	items := make([]map[string]any, len(rows))
	for i, row := range rows {
		items[i] = row.m
	}

	resp := map[string]any{
		"items":           items,
		"strava_included": stravaIncluded,
	}
	if len(rows) == limit {
		oldest := rows[len(rows)-1].at
		resp["next_before"] = oldest.Add(-time.Millisecond).UTC().Format(time.RFC3339Nano)
	}

	writeJSON(w, http.StatusOK, resp)
}

func (h *Handlers) GetLiveRun(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser{}).(*models.User)
	if !h.requireCapability(w, r, u, "live_runs") {
		return
	}
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
	m := map[string]any{
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
	if lr.ClientStats != nil {
		m["client_stats"] = lr.ClientStats
	}
	return m
}
