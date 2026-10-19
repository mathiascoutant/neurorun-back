package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"runapp/internal/models"
	"runapp/internal/store"

	"github.com/go-chi/chi/v5"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

/*
Ambassadeurs : un compte NeuroRun désigné depuis la console, son code de réduction
personnel et le contrat type signé des deux côtés.

Le contrat se signe en deux temps, et chaque temps a son propriétaire : l’ambassadeur
remplit son identité et signe depuis son compte, NeuroRun contresigne depuis la console.
Aucun des deux ne peut signer à la place de l’autre, et une signature posée n’est jamais
réécrite — c’est tout l’intérêt de la garder en base plutôt que dans un PDF qui circule.
*/

// clientIP : adresse de l’appelant, derrière un reverse proxy comme en direct.
func clientIP(r *http.Request) string {
	if xff := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); xff != "" {
		if first := strings.TrimSpace(strings.Split(xff, ",")[0]); first != "" {
			return first
		}
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

func newSignature(r *http.Request, fullName string) models.Signature {
	return models.Signature{
		FullName: fullName,
		Mention:  models.AmbassadorMention,
		SignedAt: time.Now().UTC(),
		IP:       clientIP(r),
		Agent:    strings.TrimSpace(r.Header.Get("User-Agent")),
	}
}

// promoView : un code promo tel que l’affichent la console et l’espace ambassadeur —
// avec ce qui se calcule (libellé de remise, quota épuisé) plutôt qu’à recalculer partout.
type promoView struct {
	ID             string  `json:"id"`
	Code           string  `json:"code"`
	Kind           string  `json:"kind"`
	PercentOff     int     `json:"percent_off"`
	AmountOffCents int64   `json:"amount_off_cents"`
	AmountOffEUR   float64 `json:"amount_off_eur"`
	DiscountLabel  string  `json:"discount_label"`
	MaxUses        int     `json:"max_uses"`
	Uses           int     `json:"uses"`
	UsesLeft       *int    `json:"uses_left"`
	Active         bool    `json:"active"`
	Exhausted      bool    `json:"exhausted"`
	Usable         bool    `json:"usable"`
	ExpiresAt      *string `json:"expires_at,omitempty"`
	CreatedAt      string  `json:"created_at"`
}

func newPromoView(p *models.PromoCode) *promoView {
	if p == nil {
		return nil
	}
	v := &promoView{
		ID:             p.ID.Hex(),
		Code:           p.Code,
		Kind:           p.EffectiveKind(),
		PercentOff:     p.PercentOff,
		AmountOffCents: p.AmountOffCents,
		AmountOffEUR:   p.AmountOffEUR(),
		DiscountLabel:  p.DiscountLabel(),
		MaxUses:        p.MaxUses,
		Uses:           p.Uses,
		Active:         p.Active,
		Exhausted:      p.Exhausted(),
		Usable:         p.Usable(),
		CreatedAt:      p.CreatedAt.Format(time.RFC3339),
	}
	if p.MaxUses > 0 {
		left := p.MaxUses - p.Uses
		if left < 0 {
			left = 0
		}
		v.UsesLeft = &left
	}
	if p.ExpiresAt != nil {
		s := p.ExpiresAt.Format(time.RFC3339)
		v.ExpiresAt = &s
	}
	return v
}

type ambassadorView struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	// UserID / Plan : réservés à la console (l’ambassadeur n’a pas besoin de son propre id).
	UserID string `json:"user_id,omitempty"`
	Plan   string `json:"plan,omitempty"`
	// AccountFirstName / AccountLastName : identité du compte, affichée avant signature.
	AccountFirstName string `json:"account_first_name,omitempty"`
	AccountLastName  string `json:"account_last_name,omitempty"`
	// ContractFirstName / ContractLastName : identité saisie en signant, celle qui engage.
	ContractFirstName string `json:"contract_first_name,omitempty"`
	ContractLastName  string `json:"contract_last_name,omitempty"`
	DisplayName       string `json:"display_name"`
	Instagram         string `json:"instagram,omitempty"`
	Status            string `json:"status"`
	ContractVersion   string `json:"contract_version,omitempty"`

	AmbassadorSignature *models.Signature `json:"ambassador_signature,omitempty"`
	CompanySignature    *models.Signature `json:"company_signature,omitempty"`

	Promo *promoView `json:"promo"`
	Notes string     `json:"notes,omitempty"`

	CreatedAt string  `json:"created_at"`
	UpdatedAt string  `json:"updated_at"`
	EndedAt   *string `json:"ended_at,omitempty"`
}

// newAmbassadorView : `admin` ouvre les champs internes (compte lié, offre, notes), que
// l’espace ambassadeur ne doit pas voir.
func newAmbassadorView(a *models.Ambassador, promo *models.PromoCode, acct *models.User, admin bool) ambassadorView {
	v := ambassadorView{
		ID:                  a.ID.Hex(),
		Email:               a.Email,
		ContractFirstName:   a.ContractFirstName,
		ContractLastName:    a.ContractLastName,
		Instagram:           a.Instagram,
		Status:              a.EffectiveStatus(),
		ContractVersion:     a.ContractVersion,
		AmbassadorSignature: a.AmbassadorSignature,
		CompanySignature:    a.CompanySignature,
		Promo:               newPromoView(promo),
		CreatedAt:           a.CreatedAt.Format(time.RFC3339),
		UpdatedAt:           a.UpdatedAt.Format(time.RFC3339),
	}
	if acct != nil {
		v.AccountFirstName = acct.FirstName
		v.AccountLastName = acct.LastName
	}
	/* Nom du contrat tant qu’il est signé, nom du compte avant : jamais une ligne vide. */
	if strings.TrimSpace(a.ContractFirstName+a.ContractLastName) != "" {
		v.DisplayName = a.ContractDisplayName()
	} else if acct != nil {
		v.DisplayName = models.DisplayName(acct.FirstName, acct.LastName, a.Email)
	} else {
		v.DisplayName = a.Email
	}
	if a.EndedAt != nil {
		s := a.EndedAt.Format(time.RFC3339)
		v.EndedAt = &s
	}
	if admin {
		v.UserID = a.UserID.Hex()
		v.Notes = a.Notes
		if acct != nil {
			v.Plan = acct.EffectivePlan()
		}
	}
	return v
}

// contractFor : le contrat type avec la remise du code de cet ambassadeur.
func contractFor(promo *models.PromoCode) models.Contract {
	label := ""
	if promo != nil {
		label = promo.DiscountLabel()
	}
	return models.BuildAmbassadorContract(label)
}

// loadAmbassadorPromo : le code attaché, ou nil si le contrat n’en a pas (encore).
func (h *Handlers) loadAmbassadorPromo(ctx context.Context, a *models.Ambassador) *models.PromoCode {
	if a == nil || a.PromoCodeID == nil {
		return nil
	}
	p, err := h.db.FindPromoByID(ctx, *a.PromoCodeID)
	if err != nil {
		return nil
	}
	return p
}

/* ——— Console admin ——— */

func (h *Handlers) AdminListAmbassadors(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	list, err := h.db.ListAmbassadors(ctx)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "liste"})
		return
	}

	promoIDs := make([]primitive.ObjectID, 0, len(list))
	userIDs := make([]primitive.ObjectID, 0, len(list))
	for _, a := range list {
		if a.PromoCodeID != nil {
			promoIDs = append(promoIDs, *a.PromoCodeID)
		}
		userIDs = append(userIDs, a.UserID)
	}
	promos, _ := h.db.FindPromosByIDs(ctx, promoIDs)
	accounts, _ := h.db.FindUsersByIDsForAdmin(ctx, userIDs)

	rows := make([]ambassadorView, 0, len(list))
	for i := range list {
		a := list[i]
		var promo *models.PromoCode
		if a.PromoCodeID != nil {
			if p, ok := promos[*a.PromoCodeID]; ok {
				promo = &p
			}
		}
		var acct *models.User
		if u, ok := accounts[a.UserID]; ok {
			acct = &u
		}
		rows = append(rows, newAmbassadorView(&a, promo, acct, true))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ambassadors":      rows,
		"contract_version": models.AmbassadorContractVersion,
	})
}

type adminAmbassadorPromoBody struct {
	Code           string `json:"code"`
	Kind           string `json:"kind"`
	PercentOff     int    `json:"percent_off"`
	AmountOffCents int64  `json:"amount_off_cents"`
	MaxUses        int    `json:"max_uses"`
	Active         *bool  `json:"active"`
}

type adminCreateAmbassadorBody struct {
	UserID    string                   `json:"user_id"`
	Instagram string                   `json:"instagram"`
	Notes     string                   `json:"notes"`
	Promo     adminAmbassadorPromoBody `json:"promo"`
	// GrantBenefits : applique l’article 2 tout de suite — offre Performance et accès
	// avant-première. Décoché, le compte garde son offre actuelle.
	GrantBenefits bool `json:"grant_benefits"`
}

// buildAmbassadorPromo : code promo d’un ambassadeur, validé, prêt à insérer.
func buildAmbassadorPromo(b adminAmbassadorPromoBody) (*models.PromoCode, string) {
	code := strings.ToUpper(strings.TrimSpace(b.Code))
	if code == "" {
		return nil, "code de réduction requis"
	}
	if utf8.RuneCountInString(code) > 40 {
		return nil, "code de réduction trop long"
	}
	if strings.ContainsAny(code, " \t\n") {
		return nil, "le code ne peut pas contenir d’espace"
	}
	if b.MaxUses < 0 {
		return nil, "nombre d’utilisations invalide"
	}
	p := &models.PromoCode{
		Code:           code,
		Kind:           models.NormalizePromoKind(b.Kind),
		PercentOff:     b.PercentOff,
		AmountOffCents: b.AmountOffCents,
		MaxUses:        b.MaxUses,
		Active:         true,
		CreatedAt:      time.Now().UTC(),
	}
	if b.Active != nil {
		p.Active = *b.Active
	}
	if p.EffectiveKind() == models.PromoKindAmount {
		p.PercentOff = 0
	} else {
		p.AmountOffCents = 0
	}
	if err := p.ValidateDiscount(); err != nil {
		return nil, err.Error()
	}
	return p, ""
}

func (h *Handlers) AdminCreateAmbassador(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var b adminCreateAmbassadorBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "json"})
		return
	}
	uid, err := primitive.ObjectIDFromHex(strings.TrimSpace(b.UserID))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "compte invalide"})
		return
	}
	acct, err := h.db.FindUserByID(ctx, uid)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "compte introuvable"})
		return
	}
	if _, err := h.db.FindAmbassadorByUserID(ctx, uid); err == nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "ce compte est déjà ambassadeur"})
		return
	}
	promo, errMsg := buildAmbassadorPromo(b.Promo)
	if errMsg != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": errMsg})
		return
	}
	if err := h.db.CreatePromoCode(ctx, promo); err != nil {
		if errors.Is(err, store.ErrDuplicatePromo) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "ce code de réduction existe déjà"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "création du code"})
		return
	}

	amb := models.Ambassador{
		UserID:    uid,
		Email:     acct.Email,
		Instagram: models.NormalizeInstagram(b.Instagram),
		Notes:     strings.TrimSpace(b.Notes),
		Status:    models.AmbassadorStatusPending,
	}
	promoID := promo.ID
	amb.PromoCodeID = &promoID
	if err := h.db.CreateAmbassador(ctx, &amb); err != nil {
		/* Le code venait d’être créé pour ce contrat : sans contrat, il n’a pas lieu d’être. */
		_ = h.db.DeletePromoCode(ctx, promo.ID)
		if errors.Is(err, store.ErrDuplicateAmbassador) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "ce compte est déjà ambassadeur"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "création"})
		return
	}

	if b.GrantBenefits {
		if err := h.db.UpdateUserPlan(ctx, uid, models.PlanPerformance); err != nil {
			log.Printf("ambassadeur %s : offre Performance non appliquée : %v", acct.Email, err)
		}
		if err := h.db.SetUserBetaAccess(ctx, uid, true); err != nil {
			log.Printf("ambassadeur %s : accès avant-première non appliqué : %v", acct.Email, err)
		}
		h.invalidateOfferCache()
		if refreshed, err := h.db.FindUserByID(ctx, uid); err == nil {
			acct = refreshed
		}
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"ambassador": newAmbassadorView(&amb, promo, acct, true),
		"contract":   contractFor(promo),
	})
}

func (h *Handlers) AdminGetAmbassador(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	oid, err := primitive.ObjectIDFromHex(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id invalide"})
		return
	}
	amb, err := h.db.FindAmbassadorByID(ctx, oid)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "introuvable"})
		return
	}
	promo := h.loadAmbassadorPromo(ctx, amb)
	acct, _ := h.db.FindUserByID(ctx, amb.UserID)
	writeJSON(w, http.StatusOK, map[string]any{
		"ambassador": newAmbassadorView(amb, promo, acct, true),
		"contract":   contractFor(promo),
	})
}

type adminPatchAmbassadorBody struct {
	Instagram *string                   `json:"instagram"`
	Notes     *string                   `json:"notes"`
	Status    *string                   `json:"status"`
	Promo     *adminAmbassadorPromoBody `json:"promo"`
}

// AdminPatchAmbassador : modifier le compte Instagram, les notes, la remise du code, ou
// mettre fin au contrat. Mettre fin désactive le code (article 4) ; reprendre le réactive.
func (h *Handlers) AdminPatchAmbassador(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	oid, err := primitive.ObjectIDFromHex(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id invalide"})
		return
	}
	amb, err := h.db.FindAmbassadorByID(ctx, oid)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "introuvable"})
		return
	}
	var b adminPatchAmbassadorBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "json"})
		return
	}

	patch := bson.M{}
	if b.Instagram != nil {
		/*
			Le compte Instagram figure dans le contrat signé : une fois la signature posée,
			il ne se corrige plus — il faudrait refaire signer. La console grise le champ,
			et le serveur le refuse, parce que c’est lui qui garde le document.
		*/
		if amb.AmbassadorSignature != nil && models.NormalizeInstagram(*b.Instagram) != amb.Instagram {
			writeJSON(w, http.StatusConflict, map[string]string{
				"error": "le compte Instagram fait partie du contrat signé — retire l’ambassadeur et recommence pour le changer",
			})
			return
		}
		patch["instagram"] = models.NormalizeInstagram(*b.Instagram)
	}
	if b.Notes != nil {
		patch["notes"] = strings.TrimSpace(*b.Notes)
	}

	promoActive := (*bool)(nil)
	if b.Status != nil {
		switch strings.TrimSpace(strings.ToLower(*b.Status)) {
		case models.AmbassadorStatusEnded:
			now := time.Now().UTC()
			patch["status"] = models.AmbassadorStatusEnded
			patch["ended_at"] = now
			off := false
			promoActive = &off
		case models.AmbassadorStatusActive, models.AmbassadorStatusPending, models.AmbassadorStatusSigned:
			/* Reprise : le statut réel se déduit des signatures déjà posées. */
			patch["status"] = amb.SignatureStatus()
			patch["ended_at"] = nil
			on := true
			promoActive = &on
		default:
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "statut inconnu"})
			return
		}
	}

	if b.Promo != nil {
		if amb.PromoCodeID == nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "aucun code rattaché à ce contrat"})
			return
		}
		current, err := h.db.FindPromoByID(ctx, *amb.PromoCodeID)
		if err != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "code introuvable"})
			return
		}
		next := *current
		next.Kind = models.NormalizePromoKind(b.Promo.Kind)
		next.PercentOff = b.Promo.PercentOff
		next.AmountOffCents = b.Promo.AmountOffCents
		if next.EffectiveKind() == models.PromoKindAmount {
			next.PercentOff = 0
		} else {
			next.AmountOffCents = 0
		}
		if err := next.ValidateDiscount(); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		if b.Promo.MaxUses < 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "nombre d’utilisations invalide"})
			return
		}
		promoPatch := bson.M{
			"kind":             next.EffectiveKind(),
			"percent_off":      next.PercentOff,
			"amount_off_cents": next.AmountOffCents,
			"max_uses":         b.Promo.MaxUses,
		}
		if b.Promo.Active != nil {
			promoPatch["active"] = *b.Promo.Active
		}
		if err := h.db.UpdatePromoCode(ctx, current.ID, promoPatch); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "mise à jour du code"})
			return
		}
	}

	/* Fin ou reprise de contrat : l’état du code suit celui du contrat. */
	if promoActive != nil && amb.PromoCodeID != nil {
		if err := h.db.UpdatePromoCode(ctx, *amb.PromoCodeID, bson.M{"active": *promoActive}); err != nil {
			log.Printf("ambassadeur %s : code non %s : %v", amb.Email, map[bool]string{true: "réactivé", false: "désactivé"}[*promoActive], err)
		}
	}

	if len(patch) > 0 {
		if err := h.db.UpdateAmbassador(ctx, oid, patch); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "mise à jour"})
			return
		}
	}

	updated, err := h.db.FindAmbassadorByID(ctx, oid)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	promo := h.loadAmbassadorPromo(ctx, updated)
	acct, _ := h.db.FindUserByID(ctx, updated.UserID)
	writeJSON(w, http.StatusOK, map[string]any{
		"ambassador": newAmbassadorView(updated, promo, acct, true),
		"contract":   contractFor(promo),
	})
}

type adminSignBody struct {
	FullName string `json:"full_name"`
	Mention  string `json:"mention"`
}

// AdminSignAmbassador : contre-signature NeuroRun, qui met le contrat en vigueur.
func (h *Handlers) AdminSignAmbassador(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	oid, err := primitive.ObjectIDFromHex(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id invalide"})
		return
	}
	amb, err := h.db.FindAmbassadorByID(ctx, oid)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "introuvable"})
		return
	}
	var b adminSignBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "json"})
		return
	}
	if !models.MentionMatches(b.Mention) {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "recopie la mention « " + models.AmbassadorMention + " »",
		})
		return
	}
	if amb.AmbassadorSignature == nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "l’ambassadeur n’a pas encore signé"})
		return
	}
	if amb.CompanySignature != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "contrat déjà contresigné"})
		return
	}
	if amb.EffectiveStatus() == models.AmbassadorStatusEnded {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "contrat terminé"})
		return
	}
	name := strings.TrimSpace(b.FullName)
	if name == "" {
		name = "NeuroRun"
	}
	if utf8.RuneCountInString(name) > 120 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "signature trop longue"})
		return
	}
	if err := h.db.CountersignAmbassadorContract(ctx, oid, newSignature(r, name)); err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "signature impossible"})
		return
	}
	updated, err := h.db.FindAmbassadorByID(ctx, oid)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	promo := h.loadAmbassadorPromo(ctx, updated)
	acct, _ := h.db.FindUserByID(ctx, updated.UserID)
	writeJSON(w, http.StatusOK, map[string]any{
		"ambassador": newAmbassadorView(updated, promo, acct, true),
		"contract":   contractFor(promo),
	})
}

// AdminDeleteAmbassador : retire le contrat. Le code de réduction est conservé mais
// désactivé — son compteur d’utilisations est un historique de ventes, pas un brouillon.
func (h *Handlers) AdminDeleteAmbassador(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	oid, err := primitive.ObjectIDFromHex(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id invalide"})
		return
	}
	amb, err := h.db.FindAmbassadorByID(ctx, oid)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "introuvable"})
		return
	}
	if err := h.db.DeleteAmbassador(ctx, oid); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "suppression"})
		return
	}
	if amb.PromoCodeID != nil {
		if err := h.db.UpdatePromoCode(ctx, *amb.PromoCodeID, bson.M{"active": false}); err != nil {
			log.Printf("ambassadeur %s retiré : code non désactivé : %v", amb.Email, err)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

/* ——— Espace ambassadeur (compte connecté) ——— */

// MyAmbassador : le contrat du compte connecté, ou `null` s’il n’est pas ambassadeur —
// une absence de contrat n’est pas une erreur, et la page le dit elle-même.
func (h *Handlers) MyAmbassador(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := ctx.Value(ctxUser{}).(*models.User)
	amb, err := h.db.FindAmbassadorByUserID(ctx, u.ID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSON(w, http.StatusOK, map[string]any{"ambassador": nil})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "lecture"})
		return
	}
	promo := h.loadAmbassadorPromo(ctx, amb)
	writeJSON(w, http.StatusOK, map[string]any{
		"ambassador": newAmbassadorView(amb, promo, u, false),
		"contract":   contractFor(promo),
		"mention":    models.AmbassadorMention,
	})
}

type signContractBody struct {
	LastName  string `json:"last_name"`
	FirstName string `json:"first_name"`
	Instagram string `json:"instagram"`
	Mention   string `json:"mention"`
	FullName  string `json:"full_name"`
}

// SignMyAmbassadorContract : l’ambassadeur remplit son identité et signe. Une seule fois :
// le contrat signé n’est plus modifiable, de part ni d’autre.
func (h *Handlers) SignMyAmbassadorContract(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := ctx.Value(ctxUser{}).(*models.User)
	amb, err := h.db.FindAmbassadorByUserID(ctx, u.ID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "aucun contrat d’ambassadeur sur ce compte"})
		return
	}
	if amb.AmbassadorSignature != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "contrat déjà signé"})
		return
	}
	if amb.EffectiveStatus() == models.AmbassadorStatusEnded {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "contrat terminé"})
		return
	}
	var b signContractBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "json"})
		return
	}
	lastName := strings.TrimSpace(b.LastName)
	firstName := strings.TrimSpace(b.FirstName)
	instagram := models.NormalizeInstagram(b.Instagram)
	fullName := strings.TrimSpace(b.FullName)

	if lastName == "" || firstName == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "nom et prénom requis"})
		return
	}
	if utf8.RuneCountInString(lastName) > 80 || utf8.RuneCountInString(firstName) > 80 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "nom ou prénom trop long"})
		return
	}
	if instagram == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "compte Instagram requis"})
		return
	}
	if utf8.RuneCountInString(instagram) > 60 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "compte Instagram trop long"})
		return
	}
	if !models.MentionMatches(b.Mention) {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "recopie exactement la mention « " + models.AmbassadorMention + " »",
		})
		return
	}
	if fullName == "" {
		fullName = models.DisplayName(firstName, lastName, u.Email)
	}
	if utf8.RuneCountInString(fullName) > 120 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "signature trop longue"})
		return
	}

	sig := newSignature(r, fullName)
	if err := h.db.SignAmbassadorContract(
		ctx, amb.ID, lastName, firstName, instagram, models.AmbassadorContractVersion, sig,
	); err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "signature impossible"})
		return
	}
	updated, err := h.db.FindAmbassadorByID(ctx, amb.ID)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	promo := h.loadAmbassadorPromo(ctx, updated)
	h.notifyAdminsAmbassadorSigned(u)
	writeJSON(w, http.StatusOK, map[string]any{
		"ambassador": newAmbassadorView(updated, promo, u, false),
		"contract":   contractFor(promo),
		"mention":    models.AmbassadorMention,
	})
}
