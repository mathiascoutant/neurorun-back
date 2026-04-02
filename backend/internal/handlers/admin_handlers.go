package handlers

import (
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"runapp/internal/models"
	"runapp/internal/store"

	"github.com/go-chi/chi/v5"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

func (h *Handlers) AdminMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u := r.Context().Value(ctxUser{}).(*models.User)
		if u.EffectiveRole() != models.RoleAdmin {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "accès administrateur requis"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (h *Handlers) AdminStats(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	total, err := h.db.CountUsers(ctx)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "stats"})
		return
	}
	since := time.Now().UTC().AddDate(0, 0, -7)
	recent, _ := h.db.CountUsersSince(ctx, since)
	nStd, _ := h.db.CountUsersStandard(ctx)
	nStrava, _ := h.db.CountUsersByPlan(ctx, models.PlanStrava)
	nPerf, _ := h.db.CountUsersByPlan(ctx, models.PlanPerformance)

	cfg, _ := h.db.GetOfferConfig(ctx)
	cfg.MergeDefaults()
	ps := cfg.PricesEUR["strava"]
	pp := cfg.PricesEUR["performance"]
	mrr := ps*float64(nStrava) + pp*float64(nPerf)

	signups, _ := h.db.SignupsByDayUTC(ctx, 30)
	top, _ := h.db.TopUsersByActivity(ctx, 10)

	writeJSON(w, http.StatusOK, map[string]any{
		"users_total":              total,
		"users_last_7d":            recent,
		"users_plan_standard":      nStd,
		"users_plan_strava":        nStrava,
		"users_plan_performance":   nPerf,
		"signups_by_day":           signups,
		"top_active_users":         top,
		"mrr_estimated_eur":        math.Round(mrr*100) / 100,
		"prices_eur":               cfg.PricesEUR,
		"subscribers_strava":       nStrava,
		"subscribers_performance":  nPerf,
	})
}

func (h *Handlers) AdminListUsers(w http.ResponseWriter, r *http.Request) {
	skip, _ := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("skip")), 10, 64)
	limit, _ := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("limit")), 10, 64)
	list, err := h.db.ListUsers(r.Context(), skip, limit)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "liste"})
		return
	}
	total, _ := h.db.CountUsers(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{"users": list, "total": total})
}

func (h *Handlers) AdminGetUser(w http.ResponseWriter, r *http.Request) {
	idHex := chi.URLParam(r, "id")
	oid, err := primitive.ObjectIDFromHex(idHex)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id invalide"})
		return
	}
	u, err := h.db.FindUserByID(r.Context(), oid)
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "introuvable"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "erreur"})
		return
	}
	gc, _ := h.db.CountGoalsByUser(r.Context(), oid)
	rc, _ := h.db.CountLiveRunsForUser(r.Context(), oid)
	goals, _ := h.db.ListGoalsSummariesByUser(r.Context(), oid, 30)
	runDocs, _ := h.db.ListLiveRunsByUser(r.Context(), oid, 80)
	runSummaries := make([]models.LiveRunListItem, 0, len(runDocs))
	for _, lr := range runDocs {
		runSummaries = append(runSummaries, models.LiveRunListItem{
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
	caps, _ := h.capabilitiesForUser(r.Context(), u)
	writeJSON(w, http.StatusOK, map[string]any{
		"user":        userPublic(u, caps),
		"goals_count": gc,
		"runs_count":  rc,
		"goals":       goals,
		"live_runs":   runSummaries,
	})
}

type adminPatchUserBody struct {
	Role *string `json:"role"`
	Plan *string `json:"plan"`
}

func (h *Handlers) AdminPatchUser(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPatch {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "PATCH"})
		return
	}
	idHex := chi.URLParam(r, "id")
	oid, err := primitive.ObjectIDFromHex(idHex)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id invalide"})
		return
	}
	var b adminPatchUserBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "json"})
		return
	}
	var rolePtr, planPtr *string
	if b.Role != nil {
		rk := strings.TrimSpace(strings.ToLower(*b.Role))
		if rk != models.RoleUser && rk != models.RoleAdmin {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "role invalide"})
			return
		}
		rolePtr = &rk
	}
	if b.Plan != nil {
		pk := strings.TrimSpace(strings.ToLower(*b.Plan))
		if pk != models.PlanStandard && pk != models.PlanStrava && pk != models.PlanPerformance {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "plan invalide"})
			return
		}
		planPtr = &pk
	}
	if rolePtr == nil && planPtr == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "rien à modifier"})
		return
	}
	if err := h.db.UpdateUserRolePlan(r.Context(), oid, rolePtr, planPtr); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "introuvable"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "mise à jour"})
		return
	}
	u, err := h.db.FindUserByID(r.Context(), oid)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	caps, _ := h.capabilitiesForUser(r.Context(), u)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "user": userPublic(u, caps)})
}

func (h *Handlers) AdminDeleteUser(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "DELETE"})
		return
	}
	actor := r.Context().Value(ctxUser{}).(*models.User)
	idHex := chi.URLParam(r, "id")
	oid, err := primitive.ObjectIDFromHex(idHex)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id invalide"})
		return
	}
	if actor.ID == oid {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "impossible de supprimer ton propre compte"})
		return
	}
	if err := h.db.DeleteUserCascade(r.Context(), oid); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "introuvable"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "suppression"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handlers) AdminListPromos(w http.ResponseWriter, r *http.Request) {
	list, err := h.db.ListPromoCodes(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "liste"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"promo_codes": list})
}

type adminCreatePromoBody struct {
	Code            string   `json:"code"`
	PercentOff      int      `json:"percent_off"`
	MaxUses         int      `json:"max_uses"`
	ExpiresAt       *string  `json:"expires_at"`
	Active          bool     `json:"active"`
	ApplicablePlans []string `json:"applicable_plans"`
}

func (h *Handlers) AdminCreatePromo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST"})
		return
	}
	var b adminCreatePromoBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "json"})
		return
	}
	b.Code = strings.TrimSpace(b.Code)
	if b.Code == "" || b.PercentOff < 0 || b.PercentOff > 100 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "code ou pourcentage invalide"})
		return
	}
	p := models.PromoCode{
		Code:            b.Code,
		PercentOff:      b.PercentOff,
		MaxUses:         b.MaxUses,
		Active:          b.Active,
		ApplicablePlans: b.ApplicablePlans,
		CreatedAt:       time.Now().UTC(),
	}
	if b.ExpiresAt != nil && strings.TrimSpace(*b.ExpiresAt) != "" {
		t, err := time.Parse(time.RFC3339, strings.TrimSpace(*b.ExpiresAt))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "expires_at RFC3339"})
			return
		}
		utc := t.UTC()
		p.ExpiresAt = &utc
	}
	if err := h.db.CreatePromoCode(r.Context(), &p); err != nil {
		if errors.Is(err, store.ErrDuplicatePromo) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "code déjà utilisé"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "création"})
		return
	}
	writeJSON(w, http.StatusCreated, p)
}

func (h *Handlers) AdminDeletePromo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "DELETE"})
		return
	}
	idHex := chi.URLParam(r, "id")
	oid, err := primitive.ObjectIDFromHex(idHex)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id"})
		return
	}
	if err := h.db.DeletePromoCode(r.Context(), oid); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "introuvable"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "suppression"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type adminPatchPromoBody struct {
	PercentOff      *int     `json:"percent_off"`
	MaxUses         *int     `json:"max_uses"`
	Active          *bool    `json:"active"`
	ApplicablePlans []string `json:"applicable_plans"`
}

func (h *Handlers) AdminPatchPromo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPatch {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "PATCH"})
		return
	}
	idHex := chi.URLParam(r, "id")
	oid, err := primitive.ObjectIDFromHex(idHex)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id"})
		return
	}
	var b adminPatchPromoBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "json"})
		return
	}
	patch := bson.M{}
	if b.PercentOff != nil {
		if *b.PercentOff < 0 || *b.PercentOff > 100 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "percent"})
			return
		}
		patch["percent_off"] = *b.PercentOff
	}
	if b.MaxUses != nil {
		patch["max_uses"] = *b.MaxUses
	}
	if b.Active != nil {
		patch["active"] = *b.Active
	}
	if b.ApplicablePlans != nil {
		patch["applicable_plans"] = b.ApplicablePlans
	}
	if len(patch) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "rien à modifier"})
		return
	}
	if err := h.db.UpdatePromoCode(r.Context(), oid, patch); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "introuvable"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "mise à jour"})
		return
	}
	p, err := h.db.FindPromoByID(r.Context(), oid)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (h *Handlers) AdminGetOfferConfig(w http.ResponseWriter, r *http.Request) {
	cfg, err := h.db.GetOfferConfig(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "config"})
		return
	}
	cfg.MergeDefaults()
	writeJSON(w, http.StatusOK, cfg)
}

func (h *Handlers) AdminPutOfferConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "PUT"})
		return
	}
	var cfg models.OfferConfig
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "json"})
		return
	}
	cfg.MergeDefaults()
	if err := h.db.UpsertOfferConfig(r.Context(), cfg); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "sauvegarde"})
		return
	}
	h.invalidateOfferCache()
	writeJSON(w, http.StatusOK, cfg)
}
