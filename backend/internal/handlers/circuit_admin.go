package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"runapp/internal/models"
	"runapp/internal/store"

	"github.com/go-chi/chi/v5"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// GET /admin/circuits?q=&skip=&limit=
func (h *Handlers) AdminListCircuits(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	skip, _ := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("skip")), 10, 64)
	limit, _ := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("limit")), 10, 64)
	list, total, err := h.db.AdminListCircuits(r.Context(), q, skip, limit)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "liste"})
		return
	}
	out := make([]map[string]any, 0, len(list))
	for _, c := range list {
		m := circuitSummaryJSON(&c)
		m["created_by"] = c.CreatedBy.Hex()
		out = append(out, m)
	}
	writeJSON(w, http.StatusOK, map[string]any{"circuits": out, "total": total})
}

type patchCircuitBody struct {
	Name *string `json:"name"`
}

// PATCH /admin/circuits/{id}
func (h *Handlers) AdminPatchCircuit(w http.ResponseWriter, r *http.Request) {
	idHex := chi.URLParam(r, "id")
	oid, err := primitive.ObjectIDFromHex(idHex)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id invalide"})
		return
	}
	var b patchCircuitBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "json invalide"})
		return
	}
	if b.Name == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "rien à modifier"})
		return
	}
	name, errMsg := SanitizeCircuitName(*b.Name)
	if errMsg != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": errMsg})
		return
	}
	if err := h.db.UpdateCircuitName(r.Context(), oid, name); errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "introuvable"})
		return
	} else if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "mise à jour"})
		return
	}
	c, err := h.db.FindCircuitByID(r.Context(), oid)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "circuit"})
		return
	}
	writeJSON(w, http.StatusOK, circuitSummaryJSON(c))
}

// DELETE /admin/circuits/{id}
func (h *Handlers) AdminDeleteCircuit(w http.ResponseWriter, r *http.Request) {
	idHex := chi.URLParam(r, "id")
	oid, err := primitive.ObjectIDFromHex(idHex)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id invalide"})
		return
	}
	if err := h.db.DeleteCircuit(r.Context(), oid); errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "introuvable"})
		return
	} else if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "suppression"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"ok": "1"})
}

// GET /admin/circuits/{id}/times?skip=&limit=
func (h *Handlers) AdminListCircuitTimes(w http.ResponseWriter, r *http.Request) {
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
	skip, _ := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("skip")), 10, 64)
	limit, _ := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("limit")), 10, 64)
	times, total, err := h.db.AdminListAllCircuitTimes(r.Context(), oid, skip, limit)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "temps"})
		return
	}
	rows, err := h.enrichTimes(r.Context(), times)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "temps"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"times": rows, "total": total})
}

// DELETE /admin/circuit-times/{id}
func (h *Handlers) AdminDeleteCircuitTime(w http.ResponseWriter, r *http.Request) {
	idHex := chi.URLParam(r, "id")
	oid, err := primitive.ObjectIDFromHex(idHex)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id invalide"})
		return
	}
	if err := h.db.DeleteCircuitTime(r.Context(), oid); errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "introuvable"})
		return
	} else if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "suppression"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"ok": "1"})
}

// GET /admin/circuit-times/search?first_name=&last_name=&skip=&limit=
func (h *Handlers) AdminSearchCircuitTimesByUser(w http.ResponseWriter, r *http.Request) {
	fn := strings.TrimSpace(r.URL.Query().Get("first_name"))
	ln := strings.TrimSpace(r.URL.Query().Get("last_name"))
	skip, _ := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("skip")), 10, 64)
	limit, _ := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("limit")), 10, 64)
	times, users, total, err := h.db.AdminSearchCircuitTimesByName(r.Context(), fn, ln, skip, limit)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "recherche"})
		return
	}
	uMap := make(map[primitive.ObjectID]models.User, len(users))
	for _, u := range users {
		uMap[u.ID] = u
	}
	circuitCache := make(map[primitive.ObjectID]*models.Circuit)
	rows := make([]map[string]any, 0, len(times))
	for _, t := range times {
		row := map[string]any{
			"id":           t.ID.Hex(),
			"circuit_id":   t.CircuitID.Hex(),
			"user_id":      t.UserID.Hex(),
			"duration_ms":  t.DurationMs,
			"created_at":   t.CreatedAt.UTC().Format(time.RFC3339Nano),
			"circuit_name": "",
		}
		if uu, ok := uMap[t.UserID]; ok {
			row["first_name"] = uu.FirstName
			row["last_name"] = uu.LastName
			row["email"] = uu.Email
			row["display_name"] = displayNameLeaderboard(&uu)
		}
		circ, ok := circuitCache[t.CircuitID]
		if !ok {
			circ, _ = h.db.FindCircuitByID(r.Context(), t.CircuitID)
			circuitCache[t.CircuitID] = circ
		}
		if circ != nil {
			row["circuit_name"] = circ.Name
		}
		rows = append(rows, row)
	}
	writeJSON(w, http.StatusOK, map[string]any{"times": rows, "total": total})
}
