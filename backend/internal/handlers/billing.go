package handlers

import (
	"context"
	"errors"
	"log"
	"net/http"
	"time"

	"runapp/internal/models"

	"github.com/stripe/stripe-go/v86"
)

// invoiceHistoryLimit : nombre de prélèvements listés dans l’espace compte.
const invoiceHistoryLimit = 24

type billingInvoice struct {
	ID              string    `json:"id"`
	Number          string    `json:"number,omitempty"`
	AmountPaidCents int64     `json:"amount_paid_cents"`
	Currency        string    `json:"currency"`
	Status          string    `json:"status"`
	PaidAt          time.Time `json:"paid_at"`
	HostedURL       string    `json:"hosted_url,omitempty"`
	PDFURL          string    `json:"pdf_url,omitempty"`
}

type billingState struct {
	Plan            string `json:"plan"`
	HasSubscription bool   `json:"has_subscription"`
	// Status : statut Stripe brut (active, past_due, canceled…). Vide sans abonnement.
	Status      string `json:"status,omitempty"`
	AmountCents int64  `json:"amount_cents,omitempty"`
	Currency    string `json:"currency,omitempty"`
	// NextPaymentAt : prochain prélèvement. Nul si l’abonnement se termine (résiliation demandée).
	NextPaymentAt *time.Time `json:"next_payment_at,omitempty"`
	// EndsAt : fin des droits en cas de résiliation programmée.
	EndsAt            *time.Time       `json:"ends_at,omitempty"`
	CancelAtPeriodEnd bool             `json:"cancel_at_period_end"`
	Invoices          []billingInvoice `json:"invoices"`
}

// billingStateFor relit l’abonnement chez Stripe (source de vérité), resynchronise le plan local
// au passage — filet de sécurité si un webhook s’est perdu — puis compose l’état affiché au compte.
func (h *Handlers) billingStateFor(r *http.Request, u *models.User) (*billingState, *models.User, error) {
	state := &billingState{
		Plan:     u.EffectivePlan(),
		Invoices: []billingInvoice{},
	}
	if u.Billing == nil || u.Billing.StripeSubscriptionID == "" || !h.stripeEnabled() {
		return state, u, nil
	}

	sub, err := h.stripe.Subscriptions.Get(u.Billing.StripeSubscriptionID, nil)
	if err != nil {
		// Abonnement disparu côté Stripe : on affiche l’offre locale sans planter l’espace compte.
		log.Printf("billing: abonnement %s illisible: %v", u.Billing.StripeSubscriptionID, err)
		return state, u, nil
	}

	plan := sub.Metadata["plan"]
	if plan != models.PlanStrava && plan != models.PlanPerformance {
		plan = u.Billing.Plan
	}
	refreshed := u
	if plan != "" {
		if updated, err := h.applySubscriptionState(r.Context(), u, sub, plan); err == nil {
			refreshed = updated
		}
	}

	state.Plan = refreshed.EffectivePlan()
	state.Status = string(sub.Status)
	state.AmountCents = subscriptionAmountCents(sub)
	state.Currency = "eur"
	state.CancelAtPeriodEnd = sub.CancelAtPeriodEnd
	state.HasSubscription = sub.Status != stripe.SubscriptionStatusCanceled &&
		sub.Status != stripe.SubscriptionStatusIncompleteExpired

	if end := subscriptionPeriodEnd(sub); end > 0 {
		t := time.Unix(end, 0).UTC()
		if sub.CancelAtPeriodEnd || !state.HasSubscription {
			state.EndsAt = &t
		} else {
			state.NextPaymentAt = &t
		}
	}

	if u.Billing.StripeCustomerID != "" {
		state.Invoices = h.listBillingInvoices(u.Billing.StripeCustomerID, sub.ID)
	}
	return state, refreshed, nil
}

func (h *Handlers) listBillingInvoices(customerID, subscriptionID string) []billingInvoice {
	out := []billingInvoice{}
	params := &stripe.InvoiceListParams{
		Customer:     stripe.String(customerID),
		Subscription: stripe.String(subscriptionID),
	}
	params.Limit = stripe.Int64(invoiceHistoryLimit)
	it := h.stripe.Invoices.List(params)
	for it.Next() {
		inv := it.Invoice()
		if inv == nil || inv.AmountPaid <= 0 {
			continue
		}
		item := billingInvoice{
			ID:              inv.ID,
			Number:          inv.Number,
			AmountPaidCents: inv.AmountPaid,
			Currency:        string(inv.Currency),
			Status:          string(inv.Status),
			PaidAt:          time.Unix(inv.Created, 0).UTC(),
			HostedURL:       inv.HostedInvoiceURL,
			PDFURL:          inv.InvoicePDF,
		}
		if inv.StatusTransitions != nil && inv.StatusTransitions.PaidAt > 0 {
			item.PaidAt = time.Unix(inv.StatusTransitions.PaidAt, 0).UTC()
		}
		out = append(out, item)
	}
	if err := it.Err(); err != nil {
		log.Printf("billing: liste des factures: %v", err)
	}
	return out
}

// CancelSubscriptionForDeletedAccount coupe l’abonnement immédiatement (pas à l’échéance) avant la
// suppression d’un compte : une fois le compte parti, plus rien ne relie le client Stripe à l’app,
// et les prélèvements continueraient dans le vide. Renvoie une erreur si l’annulation n’a pas pu
// aboutir — mieux vaut refuser la suppression que laisser un abonnement orphelin encaisser.
//
// Le client Stripe, lui, est conservé : les factures déjà émises relèvent des obligations
// comptables. Il est seulement marqué comme rattaché à un compte supprimé.
func (h *Handlers) CancelSubscriptionForDeletedAccount(ctx context.Context, u *models.User) error {
	if !h.stripeEnabled() || u == nil || u.Billing == nil {
		return nil
	}
	if subID := u.Billing.StripeSubscriptionID; subID != "" {
		sub, err := h.stripe.Subscriptions.Get(subID, nil)
		switch {
		case isStripeResourceMissing(err):
			// Abonnement inconnu de Stripe : rien à couper.
		case err != nil:
			return err
		case sub.Status == stripe.SubscriptionStatusCanceled ||
			sub.Status == stripe.SubscriptionStatusIncompleteExpired:
			// Déjà terminé.
		default:
			if _, err := h.stripe.Subscriptions.Cancel(subID, nil); err != nil && !isStripeResourceMissing(err) {
				return err
			}
			log.Printf("billing: abonnement %s annulé (suppression du compte %s)", subID, u.ID.Hex())
		}
	}
	if custID := u.Billing.StripeCustomerID; custID != "" {
		params := &stripe.CustomerParams{}
		params.AddMetadata("account_deleted_at", time.Now().UTC().Format(time.RFC3339))
		if _, err := h.stripe.Customers.Update(custID, params); err != nil {
			// Traçabilité seulement : ne doit pas empêcher la suppression du compte.
			log.Printf("billing: marquage client %s: %v", custID, err)
		}
	}
	return nil
}

func isStripeResourceMissing(err error) bool {
	var se *stripe.Error
	return errors.As(err, &se) && se.Code == stripe.ErrorCodeResourceMissing
}

// BillingSubscription GET — offre en cours, prochain prélèvement et historique des paiements.
func (h *Handlers) BillingSubscription(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method"})
		return
	}
	u := r.Context().Value(ctxUser{}).(*models.User)
	state, _, err := h.billingStateFor(r, u)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "abonnement illisible"})
		return
	}
	writeJSON(w, http.StatusOK, state)
}

// BillingCancel POST — résiliation à la fin de la période payée : Stripe cesse de prélever, et
// l’offre reste active jusqu’à l’échéance. La bascule en offre gratuite se fait alors via le webhook
// `customer.subscription.deleted` (ou à la prochaine lecture de cette page, par sécurité).
func (h *Handlers) BillingCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method"})
		return
	}
	h.setCancelAtPeriodEnd(w, r, true)
}

// BillingResume POST — reprise d’un abonnement résilié mais encore dans sa période payée.
func (h *Handlers) BillingResume(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method"})
		return
	}
	h.setCancelAtPeriodEnd(w, r, false)
}

func (h *Handlers) setCancelAtPeriodEnd(w http.ResponseWriter, r *http.Request, cancel bool) {
	if !h.stripeEnabled() {
		writeStripeUnavailable(w)
		return
	}
	u := r.Context().Value(ctxUser{}).(*models.User)
	if u.Billing == nil || u.Billing.StripeSubscriptionID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "aucun abonnement en cours"})
		return
	}
	sub, err := h.stripe.Subscriptions.Update(u.Billing.StripeSubscriptionID, &stripe.SubscriptionParams{
		CancelAtPeriodEnd: stripe.Bool(cancel),
	})
	if err != nil {
		log.Printf("billing: cancel_at_period_end=%v: %v", cancel, err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": stripeUserMessage(err)})
		return
	}
	plan := sub.Metadata["plan"]
	if plan != models.PlanStrava && plan != models.PlanPerformance {
		plan = u.Billing.Plan
	}
	if plan != "" {
		if updated, err := h.applySubscriptionState(r.Context(), u, sub, plan); err == nil {
			u = updated
		}
	}
	state, _, err := h.billingStateFor(r, u)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "abonnement illisible"})
		return
	}
	writeJSON(w, http.StatusOK, state)
}
