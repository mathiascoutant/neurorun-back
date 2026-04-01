package handlers

import (
	"context"
	"net/http"
	"time"

	"runapp/internal/models"
)

const offerCacheTTL = 12 * time.Second

func (h *Handlers) cachedOfferConfig(ctx context.Context) (*models.OfferConfig, error) {
	h.offerMu.RLock()
	if h.offerCache != nil && time.Now().Before(h.offerExpiry) {
		c := *h.offerCache
		h.offerMu.RUnlock()
		return &c, nil
	}
	h.offerMu.RUnlock()

	h.offerMu.Lock()
	defer h.offerMu.Unlock()
	if h.offerCache != nil && time.Now().Before(h.offerExpiry) {
		c := *h.offerCache
		return &c, nil
	}
	cfg, err := h.db.GetOfferConfig(ctx)
	if err != nil {
		return nil, err
	}
	cfg.MergeDefaults()
	h.offerCache = &cfg
	h.offerExpiry = time.Now().Add(offerCacheTTL)
	c := cfg
	return &c, nil
}

func (h *Handlers) invalidateOfferCache() {
	h.offerMu.Lock()
	h.offerCache = nil
	h.offerExpiry = time.Time{}
	h.offerMu.Unlock()
}

func (h *Handlers) capabilitiesForUser(ctx context.Context, u *models.User) (map[string]bool, error) {
	cfg, err := h.cachedOfferConfig(ctx)
	if err != nil {
		return nil, err
	}
	return cfg.CapabilitiesForPlan(u.EffectivePlan()), nil
}

func (h *Handlers) userHasCapability(ctx context.Context, u *models.User, key string) (bool, error) {
	cap, err := h.capabilitiesForUser(ctx, u)
	if err != nil {
		return false, err
	}
	return cap[key], nil
}

func writeFeatureForbidden(w http.ResponseWriter, feature string) {
	writeJSON(w, http.StatusForbidden, map[string]string{
		"error": "fonction non incluse dans ton offre actuelle (" + feature + ")",
	})
}

func (h *Handlers) requireCapability(w http.ResponseWriter, r *http.Request, u *models.User, key string) bool {
	ok, err := h.userHasCapability(r.Context(), u, key)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "config"})
		return false
	}
	if !ok {
		writeFeatureForbidden(w, key)
		return false
	}
	return true
}
