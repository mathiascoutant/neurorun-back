package handlers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"runapp/internal/models"
	"runapp/internal/store"

	appstoreapi "github.com/awa/go-iap/appstore/api"
)

// Achats intégrés iOS.
//
// L’app ne peut pas encaisser par Stripe : la règle App Store 3.1.1 impose l’achat intégré pour du
// contenu numérique consommé dans l’app. L’app iOS passe donc par StoreKit, le web par Stripe, et
// les deux circuits convergent sur le même champ `plan` du compte.
//
// Conséquences à connaître, elles ne sont pas contournables côté serveur :
//   - le prix iOS est celui déclaré dans App Store Connect ; les tarifs pilotés depuis l’admin
//     NeuroRun ne s’appliquent qu’au web ;
//   - les codes promo maison ne s’appliquent pas à un achat StoreKit (Apple n’accepte que ses
//     propres Offer Codes). Une promo 100 % reste possible : elle n’encaisse rien, donc elle passe
//     par /checkout/subscribe sans toucher Apple.

// appleStatus* : statut d’abonnement iOS stocké dans Billing.Status, en miroir des statuts Stripe.
const (
	appleStatusActive       = "active"
	appleStatusExpired      = "expired"
	appleStatusBillingRetry = "billing_retry"
	appleStatusGracePeriod  = "grace_period"
	appleStatusRevoked      = "revoked"
)

// maxAppleNotificationBody borne la lecture du corps d’une notification App Store.
const maxAppleNotificationBody = 1 << 20

var errAppleUnknownProduct = errors.New("produit App Store inconnu")

func (h *Handlers) appleEnabled() bool {
	return h.apple != nil
}

func writeAppleUnavailable(w http.ResponseWriter) {
	writeJSON(w, http.StatusServiceUnavailable, map[string]string{
		"error": "achats intégrés indisponibles — configure les clés APPLE_IAP_* côté API",
	})
}

// applePlanForProduct traduit un identifiant produit App Store Connect en offre NeuroRun.
func (h *Handlers) applePlanForProduct(productID string) (string, error) {
	productID = strings.TrimSpace(productID)
	if productID == "" {
		return "", errAppleUnknownProduct
	}
	for plan, id := range h.cfg.AppleProductIDs() {
		if id != "" && strings.EqualFold(id, productID) {
			return plan, nil
		}
	}
	return "", errAppleUnknownProduct
}

// appleEnvironmentMatches : un achat sandbox ne doit jamais ouvrir une offre en production, et
// inversement. StoreKit renvoie l’environnement dans la transaction signée ; on le compare à la
// configuration du serveur plutôt qu’à une valeur transmise par l’app.
func (h *Handlers) appleEnvironmentMatches(env appstoreapi.Environment) bool {
	if h.cfg.AppleSandbox {
		return env == appstoreapi.Sandbox
	}
	return env == appstoreapi.Production
}

// AppleIAPConfig GET — état de l’achat intégré pour l’app (elle n’a pas accès aux variables d’env).
func (h *Handlers) AppleIAPConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"apple_iap_enabled": h.appleEnabled(),
		"sandbox":           h.cfg.AppleSandbox,
		"products":          h.cfg.AppleProductIDs(),
	})
}

type appleVerifyBody struct {
	// SignedTransaction : `Transaction.jwsRepresentation` renvoyé par StoreKit 2 après l’achat.
	SignedTransaction string `json:"signed_transaction"`
}

// AppleCheckoutVerify POST — l’app envoie la transaction signée par StoreKit après un achat (ou une
// restauration). On vérifie la signature contre la CA racine d’Apple, puis on relit l’état réel de
// l’abonnement auprès de l’App Store Server API avant d’activer quoi que ce soit : la transaction
// prouve l’achat, elle ne prouve pas que l’abonnement est encore en cours.
func (h *Handlers) AppleCheckoutVerify(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method"})
		return
	}
	if !h.appleEnabled() {
		writeAppleUnavailable(w)
		return
	}
	u := r.Context().Value(ctxUser{}).(*models.User)

	var b appleVerifyBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "json invalide"})
		return
	}
	b.SignedTransaction = strings.TrimSpace(b.SignedTransaction)
	if b.SignedTransaction == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "transaction manquante"})
		return
	}

	tx, err := h.apple.ParseSignedTransaction(b.SignedTransaction)
	if err != nil {
		log.Printf("apple: transaction signée invalide: %v", err)
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "achat non vérifiable"})
		return
	}
	if !strings.EqualFold(tx.BundleID, h.cfg.AppleBundleID) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "achat émis par une autre application"})
		return
	}
	if !h.appleEnvironmentMatches(tx.Environment) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "achat effectué dans un autre environnement App Store"})
		return
	}
	if tx.OriginalTransactionId == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "transaction sans identifiant d’abonnement"})
		return
	}
	if _, err := h.applePlanForProduct(tx.ProductID); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "produit inconnu pour cette application"})
		return
	}

	// Un même abonnement iOS ne peut pas ouvrir l’offre sur deux comptes NeuroRun : c’est le cas
	// d’une restauration effectuée depuis un second compte avec le même identifiant Apple.
	switch owner, err := h.db.FindUserByAppleOriginalTransactionID(r.Context(), tx.OriginalTransactionId); {
	case err == nil && owner.ID != u.ID:
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "cet abonnement App Store est déjà rattaché à un autre compte NeuroRun",
		})
		return
	case err != nil && !errors.Is(err, store.ErrNotFound):
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "vérification impossible"})
		return
	}

	// Abonnement Stripe encore actif : on refuse plutôt que de facturer deux fois. L’utilisateur
	// résilie côté web (ou attend la fin de période) avant de reprendre l’offre depuis l’app.
	if b := u.Billing; b != nil &&
		b.EffectiveProvider() == models.BillingProviderStripe &&
		b.StripeSubscriptionID != "" &&
		stripeStatusGrantsAccess(b.Status) {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "un abonnement par carte est déjà en cours — résilie-le depuis le site avant de souscrire via l’App Store",
		})
		return
	}

	user, err := h.refreshAppleSubscription(r.Context(), u, tx.OriginalTransactionId)
	if err != nil {
		log.Printf("apple: refresh %s: %v", tx.OriginalTransactionId, err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "abonnement introuvable chez Apple"})
		return
	}
	caps, _ := h.capabilitiesForUser(r.Context(), user)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":   true,
		"user": userPublic(user, caps),
	})
}

// stripeStatusGrantsAccess : statuts Stripe pour lesquels l’offre est encore ouverte.
func stripeStatusGrantsAccess(status string) bool {
	switch status {
	case "active", "trialing", "past_due":
		return true
	}
	return false
}

// refreshAppleSubscription relit l’abonnement auprès d’Apple et aligne le compte dessus.
//
// Comme pour Stripe, on ne fait jamais confiance à ce que transmet le client : la transaction reçue
// ne sert qu’à obtenir l’identifiant d’abonnement, puis l’état vient de l’App Store Server API.
func (h *Handlers) refreshAppleSubscription(
	ctx context.Context,
	u *models.User,
	originalTxID string,
) (*models.User, error) {
	statuses, err := h.apple.GetALLSubscriptionStatuses(ctx, originalTxID, nil)
	if err != nil {
		return nil, err
	}

	item, found := lastTransactionFor(statuses, originalTxID)
	if !found {
		return nil, errors.New("aucun abonnement pour cette transaction")
	}

	tx, err := h.apple.ParseSignedTransaction(item.SignedTransactionInfo)
	if err != nil {
		return nil, err
	}
	plan, err := h.applePlanForProduct(tx.ProductID)
	if err != nil {
		return nil, err
	}

	billing := models.Billing{
		Provider:                   models.BillingProviderApple,
		AppleOriginalTransactionID: originalTxID,
		AppleProductID:             tx.ProductID,
		AppleEnvironment:           string(tx.Environment),
		Status:                     appleStatusLabel(item.Status),
		Plan:                       plan,
		AmountCents:                tx.Price / 10, // StoreKit exprime le prix en millièmes d’unité.
	}
	if u.Billing != nil {
		// Le client Stripe reste attaché au compte : il servira si l’utilisateur repasse par le web.
		billing.StripeCustomerID = u.Billing.StripeCustomerID
	}
	if tx.ExpiresDate > 0 {
		end := time.UnixMilli(tx.ExpiresDate).UTC()
		billing.CurrentPeriodEnd = &end
	}
	// L’équivalent iOS de `cancel_at_period_end` : le renouvellement automatique a été coupé, mais
	// l’offre reste ouverte jusqu’à la fin de la période payée.
	if info, err := h.apple.ParseJWSEncodeString(item.SignedRenewalInfo); err == nil {
		if renewal, ok := info.(*appstoreapi.JWSRenewalInfoDecodedPayload); ok && renewal != nil {
			billing.CancelAtPeriodEnd = renewal.AutoRenewStatus == appstoreapi.AutoRenewStatusOff
		}
	}

	return h.applyAppleSubscriptionState(ctx, u, billing, item.Status)
}

// lastTransactionFor retrouve l’abonnement visé dans la réponse d’Apple, qui renvoie tous les
// groupes d’abonnement de l’app.
func lastTransactionFor(
	statuses *appstoreapi.StatusResponse,
	originalTxID string,
) (appstoreapi.LastTransactionsItem, bool) {
	if statuses == nil {
		return appstoreapi.LastTransactionsItem{}, false
	}
	for _, group := range statuses.Data {
		for _, item := range group.LastTransactions {
			if item.OriginalTransactionId == originalTxID {
				return item, true
			}
		}
	}
	return appstoreapi.LastTransactionsItem{}, false
}

func appleStatusLabel(status appstoreapi.AutoRenewSubscriptionStatus) string {
	switch status {
	case appstoreapi.SubscriptionActive:
		return appleStatusActive
	case appstoreapi.SubscriptionExpired:
		return appleStatusExpired
	case appstoreapi.SubscriptionRetryPeriod:
		return appleStatusBillingRetry
	case appstoreapi.SubscriptionGracePeriod:
		return appleStatusGracePeriod
	case appstoreapi.SubscriptionRevoked:
		return appleStatusRevoked
	}
	return ""
}

// applyAppleSubscriptionState aligne plan + facturation sur le statut Apple, en miroir exact de
// applySubscriptionState pour Stripe.
func (h *Handlers) applyAppleSubscriptionState(
	ctx context.Context,
	u *models.User,
	billing models.Billing,
	status appstoreapi.AutoRenewSubscriptionStatus,
) (*models.User, error) {
	if err := h.db.SetUserBilling(ctx, u.ID, billing); err != nil {
		return nil, err
	}

	switch status {
	case appstoreapi.SubscriptionActive, appstoreapi.SubscriptionRetryPeriod, appstoreapi.SubscriptionGracePeriod:
		// billing_retry / grace_period : Apple relance le paiement, on laisse l’accès ouvert le
		// temps des tentatives, comme past_due côté Stripe.
		if current := u.EffectivePlan(); current != billing.Plan {
			// Bascule conditionnelle : la vérification d’achat et le webhook traitent souvent le
			// même abonnement, seul le gagnant notifie les admins.
			switched, err := h.db.SwitchUserPlan(ctx, u.ID, current, billing.Plan)
			if err != nil {
				return nil, err
			}
			if !switched {
				if cur, err := h.db.FindUserByID(ctx, u.ID); err == nil && cur.EffectivePlan() != billing.Plan {
					if err := h.db.UpdateUserPlan(ctx, u.ID, billing.Plan); err != nil {
						return nil, err
					}
					switched = true
				}
			}
			if switched {
				h.notifyAdminsPlanActivated(u, billing.Plan)
			}
		}
	case appstoreapi.SubscriptionExpired, appstoreapi.SubscriptionRevoked:
		// Rétrogradation seulement si l’offre en cours est bien celle payée par cet abonnement.
		if u.EffectivePlan() == billing.Plan {
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

// AppleNotifications POST — App Store Server Notifications V2 : source de vérité pour les
// renouvellements, résiliations, échecs de paiement et remboursements.
//
// Même principe que le webhook Stripe : on ne fait pas confiance au corps reçu. La signature de
// l’enveloppe n’est pas vérifiable avec les outils publics de go-iap, alors on ne s’en sert que
// pour extraire la transaction, qui est elle signée par Apple et vérifiée intégralement ; l’état
// appliqué est ensuite relu auprès de l’App Store Server API. Un faux appel ne peut donc rien
// activer : il faudrait produire une transaction signée par la CA racine d’Apple.
func (h *Handlers) AppleNotifications(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method"})
		return
	}
	if !h.appleEnabled() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "webhook non configuré"})
		return
	}
	payload, err := io.ReadAll(io.LimitReader(r.Body, maxAppleNotificationBody))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "corps illisible"})
		return
	}

	var envelope struct {
		SignedPayload string `json:"signedPayload"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil || envelope.SignedPayload == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "payload invalide"})
		return
	}

	notification, err := decodeAppleNotification(envelope.SignedPayload)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "notification illisible"})
		return
	}
	if notification.Data.SignedTransactionInfo == "" {
		// Notifications sans transaction (TEST, RENEWAL_EXTENSION…) : rien à synchroniser.
		writeJSON(w, http.StatusOK, map[string]bool{"received": true})
		return
	}

	// Seul ce bloc est authentifié : signature vérifiée contre la CA racine d’Apple.
	tx, err := h.apple.ParseSignedTransaction(notification.Data.SignedTransactionInfo)
	if err != nil {
		log.Printf("apple webhook %s: transaction non vérifiable: %v", notification.NotificationType, err)
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "signature invalide"})
		return
	}
	if !strings.EqualFold(tx.BundleID, h.cfg.AppleBundleID) || !h.appleEnvironmentMatches(tx.Environment) {
		// Notification d’une autre app ou du mauvais environnement : on accuse réception pour
		// qu’Apple cesse de réessayer, sans rien appliquer.
		writeJSON(w, http.StatusOK, map[string]bool{"received": true})
		return
	}

	h.syncAppleSubscription(r.Context(), tx.OriginalTransactionId, notification.NotificationType)
	writeJSON(w, http.StatusOK, map[string]bool{"received": true})
}

type appleNotificationPayload struct {
	NotificationType string `json:"notificationType"`
	Subtype          string `json:"subtype"`
	Data             struct {
		BundleID              string `json:"bundleId"`
		Environment           string `json:"environment"`
		SignedTransactionInfo string `json:"signedTransactionInfo"`
		SignedRenewalInfo     string `json:"signedRenewalInfo"`
	} `json:"data"`
}

// decodeAppleNotification lit l’enveloppe JWS sans en vérifier la signature : elle ne sert qu’à
// atteindre `signedTransactionInfo`, qui est vérifiée séparément.
func decodeAppleNotification(signedPayload string) (*appleNotificationPayload, error) {
	parts := strings.Split(signedPayload, ".")
	if len(parts) != 3 {
		return nil, errors.New("jws malformé")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, err
	}
	var payload appleNotificationPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}
	return &payload, nil
}

func (h *Handlers) syncAppleSubscription(ctx context.Context, originalTxID, notificationType string) {
	if originalTxID == "" {
		return
	}
	u, err := h.db.FindUserByAppleOriginalTransactionID(ctx, originalTxID)
	if err != nil {
		// Cas normal au tout premier achat : la notification peut devancer /checkout/apple/verify,
		// qui rattachera l’abonnement au compte juste après.
		log.Printf("apple webhook %s: aucun compte pour l’abonnement %s", notificationType, originalTxID)
		return
	}
	if _, err := h.refreshAppleSubscription(ctx, u, originalTxID); err != nil {
		log.Printf("apple webhook %s: sync %s: %v", notificationType, originalTxID, err)
	}
}
