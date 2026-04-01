package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"runapp/internal/models"
	"runapp/internal/store"
)

func (h *Handlers) PublicOfferConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method"})
		return
	}
	cfg, err := h.cachedOfferConfig(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "config"})
		return
	}
	writeJSON(w, http.StatusOK, cfg)
}

type checkoutBody struct {
	Plan     string `json:"plan"`
	PromoCode string `json:"promo_code"`
}

func checkoutPriceEUR(cfg *models.OfferConfig, plan string) (float64, bool) {
	cfg.MergeDefaults()
	p, ok := cfg.PricesEUR[plan]
	return p, ok && p > 0
}

func applyPromoPercent(base float64, percentOff int) float64 {
	if percentOff <= 0 {
		return base
	}
	if percentOff >= 100 {
		return 0
	}
	return round2(base * float64(100-percentOff) / 100)
}

func round2(x float64) float64 {
	return float64(int64(x*100+0.5)) / 100
}

func (h *Handlers) validatePromo(ctx context.Context, code string, plan string) (*models.PromoCode, error) {
	code = strings.TrimSpace(code)
	if code == "" {
		return nil, nil
	}
	p, err := h.db.FindPromoByCode(ctx, code)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, errors.New("code promo inconnu")
		}
		return nil, err
	}
	if !p.Active {
		return nil, errors.New("code promo inactif")
	}
	if p.ExpiresAt != nil && time.Now().UTC().After(*p.ExpiresAt) {
		return nil, errors.New("code promo expiré")
	}
	if p.MaxUses > 0 && p.Uses >= p.MaxUses {
		return nil, errors.New("code promo épuisé")
	}
	if len(p.ApplicablePlans) > 0 {
		ok := false
		for _, pl := range p.ApplicablePlans {
			if pl == plan {
				ok = true
				break
			}
		}
		if !ok {
			return nil, errors.New("code non valable pour cette offre")
		}
	}
	if p.PercentOff < 0 || p.PercentOff > 100 {
		return nil, errors.New("promo invalide")
	}
	return p, nil
}

// CheckoutPreview POST — authentifié : prix avec réduction éventuelle.
func (h *Handlers) CheckoutPreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method"})
		return
	}
	u := r.Context().Value(ctxUser{}).(*models.User)
	var b checkoutBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "json invalide"})
		return
	}
	b.Plan = strings.TrimSpace(strings.ToLower(b.Plan))
	if b.Plan != models.PlanStrava && b.Plan != models.PlanPerformance {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "plan invalide (strava ou performance)"})
		return
	}
	cfg, err := h.cachedOfferConfig(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "config"})
		return
	}
	base, ok := checkoutPriceEUR(cfg, b.Plan)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "prix non défini pour ce plan"})
		return
	}
	promo, err := h.validatePromo(r.Context(), b.PromoCode, b.Plan)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	pct := 0
	if promo != nil {
		pct = promo.PercentOff
	}
	final := applyPromoPercent(base, pct)
	writeJSON(w, http.StatusOK, map[string]any{
		"plan":            b.Plan,
		"base_price_eur":  base,
		"discount_percent": pct,
		"final_price_eur":  final,
		"email":           u.Email,
	})
}

// CheckoutSubscribe POST — enregistre le plan (paiement réel à brancher plus tard).
func (h *Handlers) CheckoutSubscribe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method"})
		return
	}
	u := r.Context().Value(ctxUser{}).(*models.User)
	var b checkoutBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "json invalide"})
		return
	}
	b.Plan = strings.TrimSpace(strings.ToLower(b.Plan))
	if b.Plan != models.PlanStrava && b.Plan != models.PlanPerformance {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "plan invalide"})
		return
	}
	promo, err := h.validatePromo(r.Context(), b.PromoCode, b.Plan)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	prevPlan := u.EffectivePlan()
	if err := h.db.UpdateUserPlan(r.Context(), u.ID, b.Plan); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "mise à jour impossible"})
		return
	}
	if promo != nil {
		if err := h.db.IncrementPromoUse(r.Context(), promo.ID); err != nil {
			_ = h.db.UpdateUserPlan(r.Context(), u.ID, prevPlan)
			writeJSON(w, http.StatusConflict, map[string]string{"error": "code promo plus disponible"})
			return
		}
	}
	h.invalidateOfferCache()
	refreshed, err := h.db.FindUserByID(r.Context(), u.ID)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]string{"ok": "plan mis à jour"})
		return
	}
	caps, _ := h.capabilitiesForUser(r.Context(), refreshed)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":   true,
		"user": userPublic(refreshed, caps),
	})
}
