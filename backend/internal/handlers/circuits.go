package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"runapp/internal/models"
	"runapp/internal/store"

	"github.com/go-chi/chi/v5"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

func validLatLng(p models.LatLng) bool {
	return p.Lat >= -90 && p.Lat <= 90 && p.Lng >= -180 && p.Lng <= 180 && !(p.Lat == 0 && p.Lng == 0)
}

func validateCircuitPoints(points []models.LatLng, start int) string {
	if len(points) < 3 {
		return "au moins 3 points sont requis"
	}
	if len(points) > 200 {
		return "trop de points (200 max)"
	}
	if start < 0 || start >= len(points) {
		return "point de départ invalide"
	}
	for _, p := range points {
		if !validLatLng(p) {
			return "coordonnées invalides"
		}
	}
	return ""
}

// GET /circuits/near?lat=&lng=&radius_km=
func (h *Handlers) CircuitsNear(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser{}).(*models.User)
	if !h.requireCapability(w, r, u, "circuit_tracks") {
		return
	}
	lat, _ := strconv.ParseFloat(strings.TrimSpace(r.URL.Query().Get("lat")), 64)
	lng, _ := strconv.ParseFloat(strings.TrimSpace(r.URL.Query().Get("lng")), 64)
	radiusKm, _ := strconv.ParseFloat(strings.TrimSpace(r.URL.Query().Get("radius_km")), 64)
	if radiusKm <= 0 || radiusKm > 200 {
		radiusKm = 25
	}
	if !validLatLng(models.LatLng{Lat: lat, Lng: lng}) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "lat/lng invalides"})
		return
	}
	radiusM := radiusKm * 1000
	list, err := h.db.FindCircuitsNear(r.Context(), lat, lng, radiusM, 60)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "liste"})
		return
	}
	out := make([]map[string]any, 0, len(list))
	for _, c := range list {
		out = append(out, circuitSummaryJSON(&c))
	}
	writeJSON(w, http.StatusOK, map[string]any{"circuits": out})
}

func circuitSummaryJSON(c *models.Circuit) map[string]any {
	return map[string]any{
		"id":          c.ID.Hex(),
		"name":        c.Name,
		"start_index": c.StartIndex,
		"points":      c.Points,
		"center":      c.Center,
		"created_at":  c.CreatedAt.UTC().Format(time.RFC3339Nano),
	}
}

type createCircuitBody struct {
	Name       string          `json:"name"`
	Points     []models.LatLng `json:"points"`
	StartIndex int             `json:"start_index"`
}

// POST /circuits
func (h *Handlers) CreateCircuit(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser{}).(*models.User)
	if !h.requireCapability(w, r, u, "circuit_tracks") {
		return
	}
	var b createCircuitBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "json invalide"})
		return
	}
	name, errMsg := SanitizeCircuitName(b.Name)
	if errMsg != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": errMsg})
		return
	}
	if msg := validateCircuitPoints(b.Points, b.StartIndex); msg != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
		return
	}
	c := &models.Circuit{
		Name:       name,
		Points:     b.Points,
		StartIndex: b.StartIndex,
		Center:     store.CircuitCenterForModels(b.Points),
		CreatedBy:  u.ID,
	}
	if err := h.db.InsertCircuit(r.Context(), c); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "création"})
		return
	}
	writeJSON(w, http.StatusCreated, circuitSummaryJSON(c))
}

func displayNameLeaderboard(u *models.User) string {
	fn := strings.TrimSpace(u.FirstName)
	ln := strings.TrimSpace(u.LastName)
	if fn == "" && ln == "" {
		return "Coureur"
	}
	if ln == "" {
		return fn
	}
	if fn == "" {
		return ln
	}
	r, _ := utf8.DecodeRuneInString(ln)
	return fn + " " + string(r) + "."
}

func (h *Handlers) enrichTimes(ctx context.Context, times []models.CircuitTime) ([]map[string]any, error) {
	out := make([]map[string]any, 0, len(times))
	for _, t := range times {
		row := map[string]any{
			"id":          t.ID.Hex(),
			"user_id":     t.UserID.Hex(),
			"duration_ms": t.DurationMs,
			"created_at":  t.CreatedAt.UTC().Format(time.RFC3339Nano),
		}
		uu, err := h.db.FindUserByID(ctx, t.UserID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return nil, err
		}
		if uu != nil {
			row["first_name"] = uu.FirstName
			row["last_name"] = uu.LastName
			row["display_name"] = displayNameLeaderboard(uu)
		} else {
			row["display_name"] = "Coureur"
		}
		out = append(out, row)
	}
	return out, nil
}

// GET /circuits/{id}
func (h *Handlers) GetCircuit(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser{}).(*models.User)
	if !h.requireCapability(w, r, u, "circuit_tracks") {
		return
	}
	idHex := chi.URLParam(r, "id")
	oid, err := primitive.ObjectIDFromHex(idHex)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id invalide"})
		return
	}
	c, err := h.db.FindCircuitByID(r.Context(), oid)
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "introuvable"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "circuit"})
		return
	}
	top, err := h.db.ListTopCircuitTimes(r.Context(), oid, 10)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "classement"})
		return
	}
	nPart, _ := h.db.CountDistinctCircuitParticipants(r.Context(), oid)
	nTot, _ := h.db.CountCircuitCompletions(r.Context(), oid)
	topJSON, err := h.enrichTimes(r.Context(), top)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "classement"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"circuit":               circuitSummaryJSON(c),
		"top_times":             topJSON,
		"participant_count":     nPart,
		"completion_count_total": nTot,
	})
}

type postTimeBody struct {
	DurationMs int64 `json:"duration_ms"`
}

// POST /circuits/{id}/times
func (h *Handlers) PostCircuitTime(w http.ResponseWriter, r *http.Request) {
	u := r.Context().Value(ctxUser{}).(*models.User)
	if !h.requireCapability(w, r, u, "circuit_tracks") {
		return
	}
	idHex := chi.URLParam(r, "id")
	oid, err := primitive.ObjectIDFromHex(idHex)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id invalide"})
		return
	}
	if _, err := h.db.FindCircuitByID(r.Context(), oid); errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "circuit introuvable"})
		return
	} else if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "circuit"})
		return
	}
	var b postTimeBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "json invalide"})
		return
	}
	const minMs = 10_000
	const maxMs = 24 * 3600 * 1000
	if b.DurationMs < minMs || b.DurationMs > maxMs {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "durée invalide (10 s à 24 h)"})
		return
	}
	t := &models.CircuitTime{
		CircuitID:  oid,
		UserID:     u.ID,
		DurationMs: b.DurationMs,
	}
	if err := h.db.InsertCircuitTime(r.Context(), t); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "enregistrement"})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"id":          t.ID.Hex(),
		"duration_ms": t.DurationMs,
		"created_at":  t.CreatedAt.UTC().Format(time.RFC3339Nano),
	})
}
