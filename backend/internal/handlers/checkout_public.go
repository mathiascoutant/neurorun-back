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
	Plan          string `json:"plan"`
	PromoCode     string `json:"promo_code"`
	PaymentMethod string `json:"payment_method"` // card | apple_pay — hérité ; le moyen de paiement réel est choisi dans le Payment Element Stripe.
}

func checkoutPriceEUR(cfg *models.OfferConfig, plan string) (float64, bool) {
	cfg.MergeDefaults()
	p, ok := cfg.PricesEUR[plan]
	return p, ok && p > 0
}

// applyPromo : prix à payer après remise, que le code retire un pourcentage ou des euros.
// Le calcul vit dans le modèle : le même, ici comme au moment d’encaisser.
func applyPromo(base float64, promo *models.PromoCode) float64 {
	return promo.DiscountedPriceEUR(base)
}

// promoSummary : la remise telle qu’on la renvoie aux clients (site et app).
func promoSummary(base float64, promo *models.PromoCode) map[string]any {
	final := applyPromo(base, promo)
	out := map[string]any{
		"base_price_eur":   round2(base),
		"discount_percent": 0,
		"discount_eur":     0.0,
		"discount_kind":    "",
		"final_price_eur":  final,
	}
	if promo == nil {
		return out
	}
	out["discount_kind"] = promo.EffectiveKind()
	out["discount_eur"] = promo.DiscountEUR(base)
	out["promo_code"] = promo.Code
	out["discount_label"] = promo.DiscountLabel()
	if promo.EffectiveKind() == models.PromoKindPercent {
		out["discount_percent"] = promo.PercentOff
	}
	return out
}

func round2(x float64) float64 {
	if !(x > 0) {
		return 0
	}
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
	if err := p.ValidateDiscount(); err != nil {
		return nil, errors.New("code promo invalide")
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
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "plan invalide (allure ou performance)"})
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
	out := promoSummary(base, promo)
	out["plan"] = b.Plan
	out["email"] = u.Email
	writeJSON(w, http.StatusOK, out)
}

// CheckoutSubscribe POST — active l’offre sans passer par Stripe. Réservé aux montants à 0 €
// (promo 100 %) : tout montant à encaisser doit passer par /checkout/subscription + /checkout/confirm.
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
	b.PaymentMethod = strings.TrimSpace(strings.ToLower(b.PaymentMethod))
	if b.PaymentMethod == "" {
		b.PaymentMethod = "card"
	}
	if b.PaymentMethod != "card" && b.PaymentMethod != "apple_pay" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "moyen de paiement invalide"})
		return
	}
	amountCents, promo, ok := h.checkoutAmountCents(w, r, b.Plan, b.PromoCode)
	if !ok {
		return
	}
	if amountCents > 0 {
		writeJSON(w, http.StatusPaymentRequired, map[string]string{
			"error": "paiement requis — passe par le paiement par carte pour activer cette offre",
		})
		return
	}
	refreshed, err := h.activateFreePlan(r.Context(), u, b.Plan, promo)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	caps, _ := h.capabilitiesForUser(r.Context(), refreshed)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":   true,
		"user": userPublic(refreshed, caps),
	})
}

// PaidSignupPreview POST — public : prix (et promo) avant création de compte pour offre payante.
func (h *Handlers) PaidSignupPreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method"})
		return
	}
	var b checkoutBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "json invalide"})
		return
	}
	b.Plan = strings.TrimSpace(strings.ToLower(b.Plan))
	if b.Plan != models.PlanStrava && b.Plan != models.PlanPerformance {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "plan invalide (allure ou performance)"})
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
	out := promoSummary(base, promo)
	out["plan"] = b.Plan
	writeJSON(w, http.StatusOK, out)
}
