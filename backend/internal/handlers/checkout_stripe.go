package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"math"
	"net/http"
	"strings"
	"time"
	"unicode"

	"runapp/internal/models"
	"runapp/internal/store"

	"github.com/stripe/stripe-go/v86"
	"github.com/stripe/stripe-go/v86/webhook"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// stripeMinChargeCents : minimum facturable par Stripe en EUR (0,50 €). En dessous, seul 0 € est accepté.
const stripeMinChargeCents int64 = 50

// maxWebhookBody borne la lecture du corps webhook avant vérification de signature.
const maxWebhookBody = 1 << 20

// stripeTaxCodeSaaS : « Software as a service (SaaS) — personal use ». Inutile tant que la session
// désactive Managed Payments, mais requis dès qu’une collecte de TVA est activée sur le compte.
const stripeTaxCodeSaaS = "txcd_10103000"

func eurToCents(eur float64) int64 {
	return int64(math.Round(eur * 100))
}

func (h *Handlers) stripeEnabled() bool {
	return h.stripe != nil
}

func writeStripeUnavailable(w http.ResponseWriter) {
	writeJSON(w, http.StatusServiceUnavailable, map[string]string{
		"error": "paiement indisponible — configure STRIPE_SECRET_KEY et STRIPE_PUBLISHABLE_KEY côté API",
	})
}

// PublicPaymentConfig GET — état du paiement pour le front (build statique : pas d’env au runtime).
func (h *Handlers) PublicPaymentConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"stripe_enabled":  h.stripeEnabled(),
		"publishable_key": h.cfg.StripePublishableKey,
		"currency":        "eur",
	})
}

// capitalizeFirst : « allure » → « Allure ». Sur les runes, les noms d’offre étant libres.
func capitalizeFirst(s string) string {
	r := []rune(s)
	if len(r) == 0 {
		return s
	}
	return string(unicode.ToUpper(r[0])) + string(r[1:])
}

// stripeProductName : intitulé vu par le client sur la page Stripe (« S’abonner à … »). Il suit le
// nom commercial défini en admin, pas l’identifiant technique du plan.
func (h *Handlers) stripeProductName(ctx context.Context, plan string) string {
	return "NeuroRun — offre " + capitalizeFirst(h.tierDisplayName(ctx, plan))
}

// stripeProductID : un produit Stripe par offre, créé à la volée avec un id déterministe
// (les prix sont pilotés depuis l’admin, donc envoyés en `price_data` à chaque session).
func (h *Handlers) stripeProductID(ctx context.Context, plan string) (string, error) {
	id := "neurorun_" + plan
	name := h.stripeProductName(ctx, plan)
	if p, err := h.stripe.Products.Get(id, nil); err == nil && p != nil {
		// Remise à niveau du produit déjà en place : nom renommé depuis l’admin, et code fiscal
		// absent des produits créés avant son ajout (sans lui la session est refusée).
		params := &stripe.ProductParams{}
		stale := false
		if p.Name != name {
			params.Name = stripe.String(name)
			stale = true
		}
		if p.TaxCode == nil || p.TaxCode.ID == "" {
			params.TaxCode = stripe.String(stripeTaxCodeSaaS)
			stale = true
		}
		if stale {
			if updated, uerr := h.stripe.Products.Update(id, params); uerr == nil && updated != nil {
				return updated.ID, nil
			}
		}
		return p.ID, nil
	}
	p, err := h.stripe.Products.New(&stripe.ProductParams{
		ID:      stripe.String(id),
		Name:    stripe.String(name),
		TaxCode: stripe.String(stripeTaxCodeSaaS),
	})
	if err != nil {
		// Course entre deux requêtes : le produit vient d’être créé ailleurs.
		if existing, getErr := h.stripe.Products.Get(id, nil); getErr == nil && existing != nil {
			return existing.ID, nil
		}
		return "", err
	}
	return p.ID, nil
}

// stripeCustomerID récupère (ou crée) le client Stripe du compte.
func (h *Handlers) stripeCustomerID(ctx context.Context, u *models.User) (string, error) {
	if u.Billing != nil && u.Billing.StripeCustomerID != "" {
		if c, err := h.stripe.Customers.Get(u.Billing.StripeCustomerID, nil); err == nil && c != nil && !c.Deleted {
			return c.ID, nil
		}
	}
	params := &stripe.CustomerParams{Email: stripe.String(u.Email)}
	if name := strings.TrimSpace(u.FirstName + " " + u.LastName); name != "" {
		params.Name = stripe.String(name)
	}
	params.AddMetadata("user_id", u.ID.Hex())
	c, err := h.stripe.Customers.New(params)
	if err != nil {
		return "", err
	}
	if err := h.db.SetUserStripeCustomer(ctx, u.ID, c.ID); err != nil {
		return "", err
	}
	if u.Billing == nil {
		u.Billing = &models.Billing{}
	}
	u.Billing.StripeCustomerID = c.ID
	return c.ID, nil
}

type createSessionBody struct {
	Plan      string `json:"plan"`
	PromoCode string `json:"promo_code"`
	// Origin / ReturnPath : où Stripe renvoie l’utilisateur. Validés côté serveur (pas d’open redirect).
	Origin     string `json:"origin"`
	ReturnPath string `json:"return_path"`
}

// resolveReturnOrigin n’accepte qu’une origine déjà déclarée de confiance (CORS), sinon le front officiel.
func (h *Handlers) resolveReturnOrigin(origin string) string {
	origin = strings.TrimRight(strings.TrimSpace(origin), "/")
	if origin != "" {
		for _, allowed := range h.cfg.CORSAllowed {
			if strings.EqualFold(strings.TrimRight(strings.TrimSpace(allowed), "/"), origin) {
				return origin
			}
		}
	}
	return strings.TrimRight(h.cfg.FrontendURL, "/")
}

// safeReturnPath : chemin interne uniquement (« /checkout/strava/ »), sinon on reconstruit celui du plan.
func safeReturnPath(raw, plan string) string {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") ||
		strings.ContainsAny(raw, "?#\\") {
		return "/checkout/" + plan + "/"
	}
	return raw
}

// CheckoutCreateSession POST — crée la session Stripe Checkout (page de paiement hébergée par Stripe)
// et renvoie son URL. Aucun formulaire de carte ne transite par l’app.
func (h *Handlers) CheckoutCreateSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method"})
		return
	}
	u := r.Context().Value(ctxUser{}).(*models.User)
	var b createSessionBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "json invalide"})
		return
	}
	b.Plan = strings.TrimSpace(strings.ToLower(b.Plan))
	if b.Plan != models.PlanStrava && b.Plan != models.PlanPerformance {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "plan invalide (allure ou performance)"})
		return
	}

	amountCents, promo, ok := h.checkoutAmountCents(w, r, b.Plan, b.PromoCode)
	if !ok {
		return
	}

	// Promo 100 % : rien à encaisser, on active directement (aucun passage par Stripe).
	if amountCents == 0 {
		user, err := h.activateFreePlan(r.Context(), u, b.Plan, promo)
		if err != nil {
			writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
			return
		}
		caps, _ := h.capabilitiesForUser(r.Context(), user)
		writeJSON(w, http.StatusOK, map[string]any{
			"free":         true,
			"amount_cents": 0,
			"user":         userPublic(user, caps),
		})
		return
	}

	if !h.stripeEnabled() {
		writeStripeUnavailable(w)
		return
	}
	if amountCents < stripeMinChargeCents {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "montant trop faible pour un paiement par carte (minimum 0,50 €)",
		})
		return
	}

	custID, err := h.stripeCustomerID(r.Context(), u)
	if err != nil {
		log.Printf("stripe customer: %v", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "paiement indisponible pour le moment"})
		return
	}
	productID, err := h.stripeProductID(r.Context(), b.Plan)
	if err != nil {
		log.Printf("stripe product: %v", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "paiement indisponible pour le moment"})
		return
	}

	origin := h.resolveReturnOrigin(b.Origin)
	path := safeReturnPath(b.ReturnPath, b.Plan)

	params := &stripe.CheckoutSessionParams{
		Mode:     stripe.String(string(stripe.CheckoutSessionModeSubscription)),
		Customer: stripe.String(custID),
		Locale:   stripe.String("fr"),
		// Managed Payments (Stripe marchand de référence) collecte la TVA en plus du prix affiché.
		// Incompatible avec la franchise en base de TVA (auto-entreprise, art. 293 B du CGI) :
		// on le désactive pour encaisser exactement le montant annoncé, sans taxe ajoutée.
		ManagedPayments: &stripe.CheckoutSessionManagedPaymentsParams{
			Enabled: stripe.Bool(false),
		},
		SuccessURL: stripe.String(origin + path + "?session_id={CHECKOUT_SESSION_ID}"),
		CancelURL:  stripe.String(origin + path + "?canceled=1"),
		LineItems: []*stripe.CheckoutSessionLineItemParams{{
			Quantity: stripe.Int64(1),
			PriceData: &stripe.CheckoutSessionLineItemPriceDataParams{
				Currency:   stripe.String(string(stripe.CurrencyEUR)),
				Product:    stripe.String(productID),
				UnitAmount: stripe.Int64(amountCents),
				Recurring: &stripe.CheckoutSessionLineItemPriceDataRecurringParams{
					Interval: stripe.String("month"),
				},
			},
		}},
		// La metadata doit être portée par l’abonnement : c’est elle que relisent /checkout/confirm
		// et le webhook pour savoir à quel compte et à quelle offre rattacher le paiement.
		SubscriptionData: &stripe.CheckoutSessionSubscriptionDataParams{
			Metadata: map[string]string{
				"user_id": u.ID.Hex(),
				"plan":    b.Plan,
			},
		},
	}
	params.AddMetadata("user_id", u.ID.Hex())
	params.AddMetadata("plan", b.Plan)
	if promo != nil {
		params.AddMetadata("promo_code", promo.Code)
		params.SubscriptionData.Metadata["promo_code"] = promo.Code
	}

	sess, err := h.stripe.CheckoutSessions.New(params)
	if err != nil {
		log.Printf("stripe checkout session: %v", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": stripeUserMessage(err)})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"free":         false,
		"session_id":   sess.ID,
		"url":          sess.URL,
		"amount_cents": amountCents,
		"currency":     "eur",
	})
}

type confirmSessionBody struct {
	SessionID string `json:"session_id"`
}

// CheckoutConfirm POST — au retour de Stripe : on relit la session chez Stripe (jamais le statut
// annoncé par le navigateur) et on active l’offre si elle est bien payée.
func (h *Handlers) CheckoutConfirm(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method"})
		return
	}
	if !h.stripeEnabled() {
		writeStripeUnavailable(w)
		return
	}
	u := r.Context().Value(ctxUser{}).(*models.User)
	var b confirmSessionBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "json invalide"})
		return
	}
	b.SessionID = strings.TrimSpace(b.SessionID)
	if b.SessionID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "session de paiement manquante"})
		return
	}

	params := &stripe.CheckoutSessionParams{}
	params.AddExpand("subscription")
	sess, err := h.stripe.CheckoutSessions.Get(b.SessionID, params)
	if err != nil {
		log.Printf("stripe checkout session get: %v", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "session de paiement introuvable"})
		return
	}
	// L’id de session vient du navigateur : on vérifie qu’elle appartient au compte authentifié.
	if sess.Metadata["user_id"] != u.ID.Hex() {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "paiement lié à un autre compte"})
		return
	}
	plan := sess.Metadata["plan"]
	if plan != models.PlanStrava && plan != models.PlanPerformance {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "plan invalide sur ce paiement"})
		return
	}
	if sess.PaymentStatus != stripe.CheckoutSessionPaymentStatusPaid &&
		sess.PaymentStatus != stripe.CheckoutSessionPaymentStatusNoPaymentRequired {
		writeJSON(w, http.StatusPaymentRequired, map[string]string{
			"error": "paiement non abouti — l’offre n’a pas été activée",
		})
		return
	}
	if sess.Subscription == nil || sess.Subscription.ID == "" {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "abonnement introuvable pour ce paiement"})
		return
	}
	sub, err := h.stripe.Subscriptions.Get(sess.Subscription.ID, nil)
	if err != nil {
		log.Printf("stripe subscription get: %v", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "abonnement introuvable chez Stripe"})
		return
	}

	// Promo consommée une seule fois : au premier passage réussi sur cet abonnement.
	alreadyApplied := u.Billing != nil && u.Billing.StripeSubscriptionID == sub.ID && u.EffectivePlan() == plan
	if !alreadyApplied {
		if code := strings.TrimSpace(sess.Metadata["promo_code"]); code != "" {
			if promo, perr := h.validatePromo(r.Context(), code, plan); perr == nil && promo != nil {
				_ = h.db.IncrementPromoUse(r.Context(), promo.ID)
			}
		}
	}

	user, err := h.applySubscriptionState(r.Context(), u, sub, plan)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "mise à jour impossible"})
		return
	}
	caps, _ := h.capabilitiesForUser(r.Context(), user)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":   true,
		"user": userPublic(user, caps),
	})
}

// StripeWebhook POST — source de vérité pour les renouvellements, échecs et résiliations.
func (h *Handlers) StripeWebhook(w http.ResponseWriter, r *http.Request) {
	if !h.stripeEnabled() || h.cfg.StripeWebhookSecret == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "webhook non configuré"})
		return
	}
	payload, err := io.ReadAll(io.LimitReader(r.Body, maxWebhookBody))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "corps illisible"})
		return
	}
	// IgnoreAPIVersionMismatch : le compte Stripe émet dans sa propre version d’API, plus récente que
	// celle figée dans stripe-go. On ne désérialise donc jamais l’objet du payload (sa forme peut avoir
	// changé) : on n’en lit que l’id, puis on relit l’objet via l’API, qui répond dans la version du SDK.
	event, err := webhook.ConstructEventWithOptions(
		payload,
		r.Header.Get("Stripe-Signature"),
		h.cfg.StripeWebhookSecret,
		webhook.ConstructEventOptions{IgnoreAPIVersionMismatch: true},
	)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "signature invalide"})
		return
	}

	var object struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(event.Data.Raw, &object); err != nil || object.ID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "payload invalide"})
		return
	}

	switch event.Type {
	case "checkout.session.completed":
		sess, err := h.stripe.CheckoutSessions.Get(object.ID, nil)
		if err != nil {
			log.Printf("webhook %s: session %s introuvable: %v", event.Type, object.ID, err)
			break
		}
		if sess.Subscription == nil || sess.Subscription.ID == "" {
			break
		}
		h.syncSubscriptionByID(r.Context(), sess.Subscription.ID, event.Type)
	case "customer.subscription.created", "customer.subscription.updated", "customer.subscription.deleted":
		sub, err := h.stripe.Subscriptions.Get(object.ID, nil)
		if err != nil {
			log.Printf("webhook %s: abonnement %s introuvable: %v", event.Type, object.ID, err)
			break
		}
		if event.Type == "customer.subscription.deleted" {
			sub.Status = stripe.SubscriptionStatusCanceled
		}
		h.syncSubscriptionFromWebhook(r.Context(), sub)
	case "invoice.paid", "invoice.payment_succeeded", "invoice.payment_failed":
		inv, err := h.stripe.Invoices.Get(object.ID, nil)
		if err != nil {
			log.Printf("webhook %s: facture %s introuvable: %v", event.Type, object.ID, err)
			break
		}
		subID := invoiceSubscriptionID(inv)
		if subID == "" {
			break
		}
		h.syncSubscriptionByID(r.Context(), subID, event.Type)
	}

	writeJSON(w, http.StatusOK, map[string]bool{"received": true})
}

func (h *Handlers) syncSubscriptionByID(ctx context.Context, subID string, eventType stripe.EventType) {
	sub, err := h.stripe.Subscriptions.Get(subID, nil)
	if err != nil {
		log.Printf("webhook %s: abonnement %s introuvable: %v", eventType, subID, err)
		return
	}
	h.syncSubscriptionFromWebhook(ctx, sub)
}

func (h *Handlers) syncSubscriptionFromWebhook(ctx context.Context, sub *stripe.Subscription) {
	hexID := sub.Metadata["user_id"]
	var u *models.User
	if hexID != "" {
		if oid, err := primitive.ObjectIDFromHex(hexID); err == nil {
			u, _ = h.db.FindUserByID(ctx, oid)
		}
	}
	if u == nil && sub.Customer != nil && sub.Customer.ID != "" {
		u, _ = h.db.FindUserByStripeCustomer(ctx, sub.Customer.ID)
	}
	if u == nil {
		log.Printf("webhook stripe: aucun compte pour l’abonnement %s", sub.ID)
		return
	}
	plan := sub.Metadata["plan"]
	if plan != models.PlanStrava && plan != models.PlanPerformance {
		if u.Billing != nil {
			plan = u.Billing.Plan
		}
	}
	if plan == "" {
		return
	}
	if _, err := h.applySubscriptionState(ctx, u, sub, plan); err != nil {
		log.Printf("webhook stripe: sync %s: %v", sub.ID, err)
	}
}

// applySubscriptionState aligne plan + état de facturation sur le statut Stripe et renvoie l’utilisateur relu.
func (h *Handlers) applySubscriptionState(
	ctx context.Context,
	u *models.User,
	sub *stripe.Subscription,
	plan string,
) (*models.User, error) {
	billing := models.Billing{
		Provider:             models.BillingProviderStripe,
		StripeSubscriptionID: sub.ID,
		Status:               string(sub.Status),
		Plan:                 plan,
		AmountCents:          subscriptionAmountCents(sub),
		CancelAtPeriodEnd:    sub.CancelAtPeriodEnd,
	}
	if sub.Customer != nil && sub.Customer.ID != "" {
		billing.StripeCustomerID = sub.Customer.ID
	}
	if end := subscriptionPeriodEnd(sub); end > 0 {
		t := time.Unix(end, 0).UTC()
		billing.CurrentPeriodEnd = &t
	}
	if err := h.db.SetUserBilling(ctx, u.ID, billing); err != nil {
		return nil, err
	}

	switch sub.Status {
	case stripe.SubscriptionStatusActive, stripe.SubscriptionStatusTrialing, stripe.SubscriptionStatusPastDue:
		// past_due : Stripe relance le paiement, on laisse l’accès le temps des tentatives.
		if current := u.EffectivePlan(); current != plan {
			// Bascule conditionnelle : /checkout/confirm et le webhook traitent souvent le même
			// abonnement, seul le gagnant notifie les admins.
			switched, err := h.db.SwitchUserPlan(ctx, u.ID, current, plan)
			if err != nil {
				return nil, err
			}
			if !switched {
				// Soit un autre appel a déjà appliqué l’offre, soit le champ `plan` porte une
				// valeur inattendue : on relit avant de forcer, pour ne pas notifier deux fois.
				if cur, err := h.db.FindUserByID(ctx, u.ID); err == nil && cur.EffectivePlan() != plan {
					if err := h.db.UpdateUserPlan(ctx, u.ID, plan); err != nil {
						return nil, err
					}
					switched = true
				}
			}
			if switched {
				h.notifyAdminsPlanActivated(u, plan)
			}
		}
	case stripe.SubscriptionStatusCanceled, stripe.SubscriptionStatusUnpaid, stripe.SubscriptionStatusIncompleteExpired:
		// Rétrogradation seulement si l’offre en cours est bien celle payée par cet abonnement.
		if u.EffectivePlan() == plan {
			if err := h.db.UpdateUserPlan(ctx, u.ID, models.PlanStandard); err != nil {
				return nil, err
			}
		}
	}

	h.invalidateOfferCache()
	refreshed, err := h.db.FindUserByID(ctx, u.ID)
	if err != nil {
		return u, nil
	}
	return refreshed, nil
}

// checkoutAmountCents calcule le montant à encaisser côté serveur (le client ne fixe jamais le prix).
func (h *Handlers) checkoutAmountCents(
	w http.ResponseWriter,
	r *http.Request,
	plan string,
	promoCode string,
) (int64, *models.PromoCode, bool) {
	cfg, err := h.cachedOfferConfig(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "config"})
		return 0, nil, false
	}
	base, ok := checkoutPriceEUR(cfg, plan)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "prix non défini pour ce plan"})
		return 0, nil, false
	}
	promo, err := h.validatePromo(r.Context(), promoCode, plan)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return 0, nil, false
	}
	pct := 0
	if promo != nil {
		pct = promo.PercentOff
	}
	return eurToCents(applyPromoPercent(base, pct)), promo, true
}

// activateFreePlan : activation sans paiement, réservée aux montants à 0 € (promo 100 %).
func (h *Handlers) activateFreePlan(
	ctx context.Context,
	u *models.User,
	plan string,
	promo *models.PromoCode,
) (*models.User, error) {
	prevPlan := u.EffectivePlan()
	if err := h.db.UpdateUserPlan(ctx, u.ID, plan); err != nil {
		return nil, errors.New("mise à jour impossible")
	}
	if promo != nil {
		if err := h.db.IncrementPromoUse(ctx, promo.ID); err != nil {
			_ = h.db.UpdateUserPlan(ctx, u.ID, prevPlan)
			return nil, errors.New("code promo plus disponible")
		}
	}
	if prevPlan != plan {
		h.notifyAdminsPlanActivated(u, plan)
	}
	h.invalidateOfferCache()
	refreshed, err := h.db.FindUserByID(ctx, u.ID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, errors.New("compte introuvable")
		}
		return u, nil
	}
	return refreshed, nil
}

// invoiceSubscriptionID : depuis l’API 2025-03-31, la facture porte son abonnement sous `parent`.
func invoiceSubscriptionID(inv *stripe.Invoice) string {
	if inv == nil || inv.Parent == nil || inv.Parent.SubscriptionDetails == nil {
		return ""
	}
	if sub := inv.Parent.SubscriptionDetails.Subscription; sub != nil {
		return sub.ID
	}
	return ""
}

// subscriptionPeriodEnd : depuis l’API 2025-03-31, la période est portée par les lignes d’abonnement.
func subscriptionPeriodEnd(sub *stripe.Subscription) int64 {
	if sub == nil || sub.Items == nil {
		return 0
	}
	var end int64
	for _, it := range sub.Items.Data {
		if it != nil && it.CurrentPeriodEnd > end {
			end = it.CurrentPeriodEnd
		}
	}
	return end
}

func subscriptionAmountCents(sub *stripe.Subscription) int64 {
	if sub == nil || sub.Items == nil {
		return 0
	}
	var total int64
	for _, it := range sub.Items.Data {
		if it == nil || it.Price == nil {
			continue
		}
		qty := it.Quantity
		if qty <= 0 {
			qty = 1
		}
		total += it.Price.UnitAmount * qty
	}
	return total
}

// stripeUserMessage : message Stripe lisible pour l’utilisateur, sinon message générique.
func stripeUserMessage(err error) string {
	var se *stripe.Error
	if errors.As(err, &se) && se.Msg != "" {
		return se.Msg
	}
	return "paiement indisponible pour le moment"
}
