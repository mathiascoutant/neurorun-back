package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"runapp/internal/models"
	"runapp/internal/store"

	"github.com/go-chi/chi/v5"
	"github.com/stripe/stripe-go/v86"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

const (
	shopMaxLines      = 20
	shopMaxQtyPerLine = 10
	// shopLowStock : à partir de ce seuil, la fiche prévient qu’il en reste peu.
	shopLowStock = 3
	// shopReservation : durée de réservation du stock. Stripe impose 30 minutes au minimum pour
	// l’expiration d’une session ; la marge évite de la refuser à quelques secondes près.
	shopReservation = 31 * time.Minute
	// shopStaleMargin : laisse au webhook le temps d’arriver avant de relire la session chez Stripe.
	shopStaleMargin = 5 * time.Minute
	// Fenêtre anti-abus du paiement : une session réserve du stock, et la route est publique.
	shopCheckoutWindow = 10 * time.Minute
	shopCheckoutPerIP  = 15
)

// shopCheckoutLimiter : compteur par adresse, en mémoire (une seule instance de l’API).
type shopCheckoutLimiter struct {
	mu   sync.Mutex
	hits map[string][]time.Time
}

var shopLimiter = &shopCheckoutLimiter{hits: map[string][]time.Time{}}

func (l *shopCheckoutLimiter) allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	kept := l.hits[key][:0]
	for _, t := range l.hits[key] {
		if now.Sub(t) < shopCheckoutWindow {
			kept = append(kept, t)
		}
	}
	if len(kept) >= shopCheckoutPerIP {
		l.hits[key] = kept
		return false
	}
	l.hits[key] = append(kept, now)
	return true
}

// shopLimiterKey : X-Real-IP, posé par nginx, ne peut pas être falsifié par le client. La première
// valeur de X-Forwarded-For, elle, vient de lui et permettrait de contourner la limite.
func shopLimiterKey(r *http.Request) string {
	if ip := strings.TrimSpace(r.Header.Get("X-Real-IP")); ip != "" {
		return ip
	}
	return clientIP(r)
}

var (
	shopSlugChars = regexp.MustCompile(`[^a-z0-9]+`)
	shopCountry   = regexp.MustCompile(`^[A-Z]{2}$`)
)

// shopSlug : « T-shirt Métal » → « t-shirt-metal ».
func shopSlug(raw string) string {
	t := transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC)
	clean, _, err := transform.String(t, raw)
	if err != nil {
		clean = raw
	}
	return strings.Trim(shopSlugChars.ReplaceAllString(strings.ToLower(clean), "-"), "-")
}

/* —— Vue publique —— */

type publicShopSize struct {
	Label     string `json:"label"`
	Available bool   `json:"available"`
	// Left : n’apparaît que lorsqu’il en reste peu. Le stock exact reste une donnée interne.
	Left int `json:"left,omitempty"`
}

type publicShopProduct struct {
	ID          string `json:"id"`
	Slug        string `json:"slug"`
	Name        string `json:"name"`
	Description string `json:"description"`
	PriceCents  int64  `json:"price_cents"`
	// ListPriceCents : prix barré, seulement pendant une promotion.
	ListPriceCents int64              `json:"list_price_cents,omitempty"`
	PromoEndsAt    *time.Time         `json:"promo_ends_at,omitempty"`
	Sizes          []publicShopSize   `json:"sizes"`
	Media          []models.ShopMedia `json:"media"`
	SoldOut        bool               `json:"sold_out"`
}

func toPublicShopProduct(p *models.ShopProduct, now time.Time) publicShopProduct {
	out := publicShopProduct{
		ID:          p.ID.Hex(),
		Slug:        p.Slug,
		Name:        p.Name,
		Description: p.Description,
		PriceCents:  p.CurrentPriceCents(now),
		Media:       p.Media,
		Sizes:       make([]publicShopSize, 0, len(p.Sizes)),
		SoldOut:     true,
	}
	if out.Media == nil {
		out.Media = []models.ShopMedia{}
	}
	if p.PromoActive(now) {
		out.ListPriceCents = p.PriceCents
		out.PromoEndsAt = p.PromoEndsAt
	}
	for _, s := range p.Sizes {
		size := publicShopSize{Label: s.Label, Available: s.Stock > 0}
		if s.Stock > 0 {
			out.SoldOut = false
			if s.Stock <= shopLowStock {
				size.Left = s.Stock
			}
		}
		out.Sizes = append(out.Sizes, size)
	}
	return out
}

// PublicShop GET — catalogue et état des ventes.
func (h *Handlers) PublicShop(w http.ResponseWriter, r *http.Request) {
	settings, err := h.db.GetShopSettings(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "boutique indisponible"})
		return
	}
	products, err := h.db.ListShopProducts(r.Context(), true)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "boutique indisponible"})
		return
	}
	now := time.Now().UTC()
	out := make([]publicShopProduct, 0, len(products))
	for i := range products {
		out = append(out, toPublicShopProduct(&products[i], now))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"sales_open":     settings.SalesOpen && h.stripeEnabled(),
		"shipping_cents": settings.ShippingCents,
		"countries":      settings.Countries,
		"products":       out,
	})
}

// PublicShopProduct GET — fiche d’un article publié.
func (h *Handlers) PublicShopProduct(w http.ResponseWriter, r *http.Request) {
	p, err := h.db.GetShopProductBySlug(r.Context(), chi.URLParam(r, "slug"))
	if err != nil || !p.Active {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "article introuvable"})
		return
	}
	settings, err := h.db.GetShopSettings(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "boutique indisponible"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"sales_open": settings.SalesOpen && h.stripeEnabled(),
		"product":    toPublicShopProduct(p, time.Now().UTC()),
	})
}

// PublicShopTerms GET — conditions générales de vente.
func (h *Handlers) PublicShopTerms(w http.ResponseWriter, r *http.Request) {
	settings, err := h.db.GetShopSettings(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "boutique indisponible"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"terms": settings.Terms})
}

/* —— Panier —— */

type shopCartItem struct {
	ProductID string `json:"product_id"`
	Size      string `json:"size"`
	Quantity  int    `json:"quantity"`
}

type shopCartBody struct {
	Items []shopCartItem `json:"items"`
	// Origin : où Stripe renvoie le client, validé contre la liste CORS.
	Origin string `json:"origin"`
}

type shopQuoteLine struct {
	ProductID      string `json:"product_id"`
	Slug           string `json:"slug"`
	Name           string `json:"name"`
	Size           string `json:"size"`
	Quantity       int    `json:"quantity"`
	UnitCents      int64  `json:"unit_cents"`
	ListPriceCents int64  `json:"list_price_cents,omitempty"`
	MediaID        string `json:"media_id,omitempty"`
	// Available : quantité réellement disponible (plafonnée à la quantité maximale par ligne).
	Available int `json:"available"`
	// Issue : « gone » (article retiré ou taille supprimée), « short » (moins de stock que demandé).
	Issue string `json:"issue,omitempty"`
}

// shopMergeItems regroupe les lignes identiques et écarte les quantités invalides. Le panier vient
// du navigateur : rien de ce qu’il contient n’est tenu pour vrai.
func shopMergeItems(items []shopCartItem) []shopCartItem {
	out := make([]shopCartItem, 0, len(items))
	index := map[string]int{}
	for _, it := range items {
		if it.Quantity <= 0 {
			continue
		}
		key := it.ProductID + "|" + it.Size
		if i, ok := index[key]; ok {
			out[i].Quantity += it.Quantity
			continue
		}
		index[key] = len(out)
		out = append(out, it)
	}
	for i := range out {
		if out[i].Quantity > shopMaxQtyPerLine {
			out[i].Quantity = shopMaxQtyPerLine
		}
	}
	return out
}

// shopQuote recalcule le panier avec les prix et stocks du serveur.
func (h *Handlers) shopQuote(ctx context.Context, items []shopCartItem) ([]shopQuoteLine, []*models.ShopProduct, error) {
	now := time.Now().UTC()
	lines := make([]shopQuoteLine, 0, len(items))
	products := make([]*models.ShopProduct, 0, len(items))
	for _, it := range items {
		line := shopQuoteLine{ProductID: it.ProductID, Size: it.Size, Quantity: it.Quantity, Issue: "gone"}
		var product *models.ShopProduct
		if oid, err := primitive.ObjectIDFromHex(it.ProductID); err == nil {
			p, err := h.db.GetShopProduct(ctx, oid)
			switch {
			case err == nil && p.Active:
				product = p
			case err != nil && !errors.Is(err, store.ErrNotFound):
				return nil, nil, err
			}
		}
		if product != nil {
			line.Slug = product.Slug
			line.Name = product.Name
			line.UnitCents = product.CurrentPriceCents(now)
			if product.PromoActive(now) {
				line.ListPriceCents = product.PriceCents
			}
			if len(product.Media) > 0 {
				line.MediaID = shopFirstImage(product.Media)
			}
			if stock, ok := product.StockFor(it.Size); ok {
				line.Available = min(stock, shopMaxQtyPerLine)
				line.Issue = ""
				if stock < it.Quantity {
					line.Issue = "short"
				}
			}
		}
		lines = append(lines, line)
		products = append(products, product)
	}
	return lines, products, nil
}

func shopFirstImage(media []models.ShopMedia) string {
	for _, m := range media {
		if m.Kind == models.ShopMediaImage {
			return m.ID
		}
	}
	return ""
}

// PublicShopQuote POST — panier recalculé, pour afficher les vrais prix et stocks.
func (h *Handlers) PublicShopQuote(w http.ResponseWriter, r *http.Request) {
	var b shopCartBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&b); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "panier invalide"})
		return
	}
	items := shopMergeItems(b.Items)
	if len(items) > shopMaxLines {
		items = items[:shopMaxLines]
	}
	lines, _, err := h.shopQuote(r.Context(), items)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "boutique indisponible"})
		return
	}
	settings, err := h.db.GetShopSettings(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "boutique indisponible"})
		return
	}
	var subtotal int64
	for _, l := range lines {
		if l.Issue != "gone" {
			subtotal += l.UnitCents * int64(l.Quantity)
		}
	}
	shipping := settings.ShippingCents
	if subtotal == 0 {
		shipping = 0
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"lines":          lines,
		"subtotal_cents": subtotal,
		"shipping_cents": shipping,
		"total_cents":    subtotal + shipping,
		"sales_open":     settings.SalesOpen && h.stripeEnabled(),
		"has_terms":      strings.TrimSpace(settings.Terms) != "",
	})
}

/* —— Paiement —— */

func (h *Handlers) releaseShopOrderStock(ctx context.Context, items []models.ShopOrderItem) {
	for _, it := range items {
		if err := h.db.ReleaseShopStock(ctx, it.ProductID, it.Size, it.Quantity); err != nil {
			log.Printf("boutique: stock non rendu (%s %s x%d): %v", it.ProductID.Hex(), it.Size, it.Quantity, err)
		}
	}
}

// PublicShopCheckout POST — réserve le stock, crée la commande et la session de paiement Stripe.
// Le montant est calculé ici : le navigateur n’envoie que des identifiants et des quantités.
func (h *Handlers) PublicShopCheckout(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if !shopLimiter.allow(shopLimiterKey(r), time.Now()) {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "trop de tentatives, réessaie dans quelques minutes"})
		return
	}
	var b shopCartBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&b); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "panier invalide"})
		return
	}
	settings, err := h.db.GetShopSettings(ctx)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "boutique indisponible"})
		return
	}
	if !settings.SalesOpen {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "les ventes ne sont pas encore ouvertes", "code": "shop_closed"})
		return
	}
	if !h.stripeEnabled() {
		writeStripeUnavailable(w)
		return
	}
	items := shopMergeItems(b.Items)
	if len(items) == 0 || len(items) > shopMaxLines {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "panier vide ou trop grand"})
		return
	}

	h.sweepStaleShopOrders(ctx)

	lines, products, err := h.shopQuote(ctx, items)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "boutique indisponible"})
		return
	}
	orderItems := make([]models.ShopOrderItem, 0, len(lines))
	var subtotal int64
	for i, l := range lines {
		if l.Issue != "" || products[i] == nil {
			writeJSON(w, http.StatusConflict, map[string]any{
				"error": "un article de ton panier n’est plus disponible dans cette quantité",
				"code":  "out_of_stock",
			})
			return
		}
		orderItems = append(orderItems, models.ShopOrderItem{
			ProductID: products[i].ID,
			Name:      l.Name,
			Size:      l.Size,
			Quantity:  l.Quantity,
			UnitCents: l.UnitCents,
		})
		subtotal += l.UnitCents * int64(l.Quantity)
	}
	if subtotal < stripeMinChargeCents {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "montant trop faible pour un paiement par carte"})
		return
	}

	// Réservation ligne par ligne : au premier refus, on rend tout ce qui a déjà été pris.
	for i, it := range orderItems {
		ok, err := h.db.ReserveShopStock(ctx, it.ProductID, it.Size, it.Quantity)
		if err != nil || !ok {
			h.releaseShopOrderStock(ctx, orderItems[:i])
			if err != nil {
				log.Printf("boutique: réservation: %v", err)
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "boutique indisponible"})
				return
			}
			writeJSON(w, http.StatusConflict, map[string]any{
				"error": it.Name + " en taille " + it.Size + " vient d’être épuisé",
				"code":  "out_of_stock",
			})
			return
		}
	}

	now := time.Now().UTC()
	order := &models.ShopOrder{
		Status:        models.ShopOrderPending,
		Items:         orderItems,
		SubtotalCents: subtotal,
		ShippingCents: settings.ShippingCents,
		TotalCents:    subtotal + settings.ShippingCents,
		ExpiresAt:     now.Add(shopReservation),
		CreatedAt:     now,
	}
	if err := h.db.CreateShopOrder(ctx, order); err != nil {
		h.releaseShopOrderStock(ctx, orderItems)
		log.Printf("boutique: création commande: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "boutique indisponible"})
		return
	}

	sess, err := h.stripe.CheckoutSessions.New(h.shopSessionParams(order, products, settings, b.Origin))
	if err != nil {
		h.releaseShopOrderStock(ctx, orderItems)
		_, _ = h.db.TransitionShopOrder(ctx, order.ID, models.ShopOrderPending, models.ShopOrderCanceled, nil)
		log.Printf("boutique: session stripe: %v", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": stripeUserMessage(err)})
		return
	}
	if err := h.db.SetShopOrderSession(ctx, order.ID, sess.ID); err != nil {
		log.Printf("boutique: session %s non enregistrée sur la commande %s: %v", sess.ID, order.ID.Hex(), err)
	}
	writeJSON(w, http.StatusOK, map[string]string{"url": sess.URL})
}

// shopSessionParams : paiement unique par carte, adresse de livraison collectée par Stripe.
func (h *Handlers) shopSessionParams(
	order *models.ShopOrder,
	products []*models.ShopProduct,
	settings models.ShopSettings,
	originRaw string,
) *stripe.CheckoutSessionParams {
	origin := h.resolveReturnOrigin(originRaw)
	countries := make([]*string, 0, len(settings.Countries))
	for _, c := range settings.Countries {
		countries = append(countries, stripe.String(c))
	}
	lineItems := make([]*stripe.CheckoutSessionLineItemParams, 0, len(order.Items))
	for i, it := range order.Items {
		data := &stripe.CheckoutSessionLineItemPriceDataProductDataParams{
			Name: stripe.String(it.Name + " — taille " + it.Size),
		}
		// Stripe doit pouvoir lire l’image : inutile en local, où l’adresse n’est pas publique.
		if id := shopFirstImage(products[i].Media); id != "" && strings.HasPrefix(origin, "https://") {
			data.Images = []*string{stripe.String(origin + "/api/public/shop/media/" + id)}
		}
		lineItems = append(lineItems, &stripe.CheckoutSessionLineItemParams{
			Quantity: stripe.Int64(int64(it.Quantity)),
			PriceData: &stripe.CheckoutSessionLineItemPriceDataParams{
				Currency:    stripe.String(string(stripe.CurrencyEUR)),
				UnitAmount:  stripe.Int64(it.UnitCents),
				ProductData: data,
			},
		})
	}
	params := &stripe.CheckoutSessionParams{
		Mode:   stripe.String(string(stripe.CheckoutSessionModePayment)),
		Locale: stripe.String("fr"),
		// Même réglage que les abonnements : montant annoncé = montant encaissé (franchise de TVA).
		ManagedPayments: &stripe.CheckoutSessionManagedPaymentsParams{Enabled: stripe.Bool(false)},
		// Carte uniquement : un moyen de paiement différé garderait le stock sans garantie.
		PaymentMethodTypes: []*string{stripe.String("card")},
		LineItems:          lineItems,
		ExpiresAt:          stripe.Int64(order.ExpiresAt.Unix()),
		SuccessURL:         stripe.String(origin + "/boutique/merci/?session_id={CHECKOUT_SESSION_ID}"),
		CancelURL:          stripe.String(origin + "/boutique/panier/"),
		ShippingAddressCollection: &stripe.CheckoutSessionShippingAddressCollectionParams{
			AllowedCountries: countries,
		},
		ShippingOptions: []*stripe.CheckoutSessionShippingOptionParams{{
			ShippingRateData: &stripe.CheckoutSessionShippingOptionShippingRateDataParams{
				Type:        stripe.String("fixed_amount"),
				DisplayName: stripe.String("Livraison"),
				FixedAmount: &stripe.CheckoutSessionShippingOptionShippingRateDataFixedAmountParams{
					Amount:   stripe.Int64(order.ShippingCents),
					Currency: stripe.String(string(stripe.CurrencyEUR)),
				},
			},
		}},
	}
	params.AddMetadata(shopOrderMetadata, order.ID.Hex())
	return params
}

// shopOrderMetadata : marque une session Stripe comme venant de la boutique (et non d’un abonnement).
const shopOrderMetadata = "shop_order_id"

func shopOrderIDFromSession(sess *stripe.CheckoutSession) (primitive.ObjectID, bool) {
	if sess == nil {
		return primitive.NilObjectID, false
	}
	oid, err := primitive.ObjectIDFromHex(sess.Metadata[shopOrderMetadata])
	return oid, err == nil
}

// finalizeShopSession enregistre un paiement abouti. Appelée par le retour du client et par le
// webhook : la transition conditionnelle garantit qu’une seule des deux notifie l’admin.
func (h *Handlers) finalizeShopSession(ctx context.Context, sess *stripe.CheckoutSession) (*models.ShopOrder, error) {
	oid, ok := shopOrderIDFromSession(sess)
	if !ok {
		return nil, store.ErrNotFound
	}
	if sess.PaymentStatus == stripe.CheckoutSessionPaymentStatusPaid {
		now := time.Now().UTC()
		extra := bson.M{"paid_at": now, "stripe_session_id": sess.ID}
		if sess.CustomerDetails != nil && sess.CustomerDetails.Email != "" {
			extra["email"] = sess.CustomerDetails.Email
		}
		if addr := shopAddressFromSession(sess); addr != nil {
			extra["address"] = addr
		}
		won, err := h.db.TransitionShopOrder(ctx, oid, models.ShopOrderPending, models.ShopOrderPaid, extra)
		if err != nil {
			return nil, err
		}
		if won {
			if order, err := h.db.GetShopOrder(ctx, oid); err == nil {
				h.notifyAdminsShopOrder(order)
			}
		}
	}
	return h.db.GetShopOrder(ctx, oid)
}

func shopAddressFromSession(sess *stripe.CheckoutSession) *models.ShopAddress {
	if sess.CollectedInformation == nil || sess.CollectedInformation.ShippingDetails == nil {
		return nil
	}
	d := sess.CollectedInformation.ShippingDetails
	if d.Address == nil {
		return nil
	}
	return &models.ShopAddress{
		Name:       d.Name,
		Line1:      d.Address.Line1,
		Line2:      d.Address.Line2,
		PostalCode: d.Address.PostalCode,
		City:       d.Address.City,
		Country:    d.Address.Country,
	}
}

// expireShopOrder annule une commande non payée et rend son stock, une seule fois.
func (h *Handlers) expireShopOrder(ctx context.Context, oid primitive.ObjectID) {
	won, err := h.db.TransitionShopOrder(ctx, oid, models.ShopOrderPending, models.ShopOrderExpired, nil)
	if err != nil {
		log.Printf("boutique: expiration %s: %v", oid.Hex(), err)
		return
	}
	if !won {
		return
	}
	if order, err := h.db.GetShopOrder(ctx, oid); err == nil {
		h.releaseShopOrderStock(ctx, order.Items)
	}
}

// sweepStaleShopOrders : filet de sécurité si le webhook n’arrive pas. On relit la session chez
// Stripe avant de décider : une commande payée ne doit jamais perdre son stock.
func (h *Handlers) sweepStaleShopOrders(ctx context.Context) {
	if !h.stripeEnabled() {
		return
	}
	stale, err := h.db.ListStaleShopOrders(ctx, time.Now().UTC().Add(-shopStaleMargin), 20)
	if err != nil {
		log.Printf("boutique: lecture commandes en attente: %v", err)
		return
	}
	for i := range stale {
		o := &stale[i]
		if o.StripeSessionID == "" {
			h.expireShopOrder(ctx, o.ID)
			continue
		}
		sess, err := h.stripe.CheckoutSessions.Get(o.StripeSessionID, nil)
		if err != nil {
			log.Printf("boutique: session %s illisible: %v", o.StripeSessionID, err)
			continue
		}
		if sess.PaymentStatus == stripe.CheckoutSessionPaymentStatusPaid {
			if _, err := h.finalizeShopSession(ctx, sess); err != nil {
				log.Printf("boutique: finalisation %s: %v", o.ID.Hex(), err)
			}
			continue
		}
		if sess.Status == stripe.CheckoutSessionStatusExpired {
			h.expireShopOrder(ctx, o.ID)
		}
	}
}

// PublicShopOrderConfirm GET — au retour de Stripe. L’identifiant de session, impossible à deviner,
// sert de preuve : on relit la session chez Stripe et on ne renvoie que le récapitulatif.
func (h *Handlers) PublicShopOrderConfirm(w http.ResponseWriter, r *http.Request) {
	if !h.stripeEnabled() {
		writeStripeUnavailable(w)
		return
	}
	sessionID := strings.TrimSpace(r.URL.Query().Get("session_id"))
	if sessionID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "session de paiement manquante"})
		return
	}
	sess, err := h.stripe.CheckoutSessions.Get(sessionID, nil)
	if err != nil {
		log.Printf("boutique: session get: %v", err)
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "paiement introuvable"})
		return
	}
	order, err := h.finalizeShopSession(r.Context(), sess)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "commande introuvable"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"paid":           order.Status == models.ShopOrderPaid || order.Status == models.ShopOrderShipped,
		"reference":      shopOrderReference(order),
		"items":          order.Items,
		"subtotal_cents": order.SubtotalCents,
		"shipping_cents": order.ShippingCents,
		"total_cents":    order.TotalCents,
		"email":          order.Email,
	})
}

// shopOrderReference : référence courte à donner au support (« NR-3F9A2C »).
func shopOrderReference(o *models.ShopOrder) string {
	hex := o.ID.Hex()
	return "NR-" + strings.ToUpper(hex[len(hex)-6:])
}

// notifyAdminsShopOrder : une vente à expédier. Même canal que les autres alertes admin.
func (h *Handlers) notifyAdminsShopOrder(o *models.ShopOrder) {
	count := 0
	for _, it := range o.Items {
		count += it.Quantity
	}
	h.notifyAdminsText(models.AdminEventShopOrder,
		"Nouvelle commande boutique",
		shopOrderReference(o)+" : "+shopArticles(count)+", "+formatShopEUR(o.TotalCents)+" à expédier",
		o.Email,
	)
}

func shopArticles(n int) string {
	if n > 1 {
		return strconv.Itoa(n) + " articles"
	}
	return "1 article"
}

// formatShopEUR : 3500 → « 35,00 € ».
func formatShopEUR(cents int64) string {
	return strings.Replace(fmt.Sprintf("%.2f", float64(cents)/100), ".", ",", 1) + " €"
}

/* —— Admin : produits —— */

type adminShopProductBody struct {
	Slug            *string            `json:"slug"`
	Name            *string            `json:"name"`
	Description     *string            `json:"description"`
	Active          *bool              `json:"active"`
	PriceCents      *int64             `json:"price_cents"`
	PromoPriceCents *int64             `json:"promo_price_cents"`
	PromoEndsAt     *string            `json:"promo_ends_at"`
	Sizes           *[]models.ShopSize `json:"sizes"`
	// MediaOrder : identifiants des médias dans le nouvel ordre.
	MediaOrder *[]string `json:"media_order"`
}

func cleanShopSizes(sizes []models.ShopSize) ([]models.ShopSize, error) {
	out := make([]models.ShopSize, 0, len(sizes))
	seen := map[string]bool{}
	for _, s := range sizes {
		label := strings.TrimSpace(s.Label)
		if label == "" {
			continue
		}
		if utf8.RuneCountInString(label) > 20 {
			return nil, errors.New("nom de taille trop long")
		}
		if seen[label] {
			return nil, errors.New("la taille " + label + " apparaît deux fois")
		}
		if s.Stock < 0 {
			return nil, errors.New("le stock ne peut pas être négatif")
		}
		seen[label] = true
		out = append(out, models.ShopSize{Label: label, Stock: s.Stock})
	}
	return out, nil
}

// reorderShopMedia applique le nouvel ordre sans jamais perdre ni inventer un média : ceux qui
// manquent à la liste reçue restent à la fin.
func reorderShopMedia(media []models.ShopMedia, order []string) []models.ShopMedia {
	byID := make(map[string]models.ShopMedia, len(media))
	for _, m := range media {
		byID[m.ID] = m
	}
	out := make([]models.ShopMedia, 0, len(media))
	for _, id := range order {
		if m, ok := byID[id]; ok {
			out = append(out, m)
			delete(byID, id)
		}
	}
	for _, m := range media {
		if _, left := byID[m.ID]; left {
			out = append(out, m)
		}
	}
	return out
}

// applyShopProductBody valide puis recopie les champs reçus. Un article publié doit pouvoir être
// acheté : prix et image sont exigés dès qu’il est actif.
func applyShopProductBody(p *models.ShopProduct, b *adminShopProductBody) error {
	if b.Name != nil {
		p.Name = strings.TrimSpace(*b.Name)
	}
	if p.Name == "" || utf8.RuneCountInString(p.Name) > 120 {
		return errors.New("nom requis (120 caractères maximum)")
	}
	if b.Slug != nil {
		p.Slug = shopSlug(*b.Slug)
	}
	if p.Slug == "" {
		p.Slug = shopSlug(p.Name)
	}
	if p.Slug == "" {
		return errors.New("adresse de l’article invalide")
	}
	if b.Description != nil {
		if utf8.RuneCountInString(*b.Description) > 5000 {
			return errors.New("description trop longue (5000 caractères maximum)")
		}
		p.Description = *b.Description
	}
	if b.PriceCents != nil {
		if *b.PriceCents < 0 {
			return errors.New("prix invalide")
		}
		p.PriceCents = *b.PriceCents
	}
	if b.PromoPriceCents != nil {
		if *b.PromoPriceCents < 0 {
			return errors.New("prix promotionnel invalide")
		}
		p.PromoPriceCents = *b.PromoPriceCents
	}
	if p.PromoPriceCents > 0 && p.PromoPriceCents >= p.PriceCents {
		return errors.New("le prix promotionnel doit être inférieur au prix normal")
	}
	if b.PromoEndsAt != nil {
		if strings.TrimSpace(*b.PromoEndsAt) == "" {
			p.PromoEndsAt = nil
		} else {
			t, err := time.Parse(time.RFC3339, *b.PromoEndsAt)
			if err != nil {
				return errors.New("date de fin de promotion invalide")
			}
			t = t.UTC()
			p.PromoEndsAt = &t
		}
	}
	if b.Sizes != nil {
		sizes, err := cleanShopSizes(*b.Sizes)
		if err != nil {
			return err
		}
		p.Sizes = sizes
	}
	if b.MediaOrder != nil {
		p.Media = reorderShopMedia(p.Media, *b.MediaOrder)
	}
	if b.Active != nil {
		p.Active = *b.Active
	}
	if p.Active {
		if p.PriceCents < stripeMinChargeCents {
			return errors.New("pour publier : indique un prix d’au moins 0,50 €")
		}
		if shopFirstImage(p.Media) == "" {
			return errors.New("pour publier : ajoute au moins une image")
		}
		if len(p.Sizes) == 0 {
			return errors.New("pour publier : ajoute au moins une taille")
		}
	}
	return nil
}

func (h *Handlers) AdminListShopProducts(w http.ResponseWriter, r *http.Request) {
	products, err := h.db.ListShopProducts(r.Context(), false)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "lecture impossible"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"products": products})
}

func (h *Handlers) AdminCreateShopProduct(w http.ResponseWriter, r *http.Request) {
	var b adminShopProductBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "json invalide"})
		return
	}
	now := time.Now().UTC()
	p := &models.ShopProduct{Sizes: []models.ShopSize{}, Media: []models.ShopMedia{}, CreatedAt: now, UpdatedAt: now}
	if err := applyShopProductBody(p, &b); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	position, err := h.db.NextShopProductPosition(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "création impossible"})
		return
	}
	p.Position = position
	if err := h.db.CreateShopProduct(r.Context(), p); err != nil {
		if errors.Is(err, store.ErrShopSlugTaken) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "un article porte déjà ce nom : change son adresse"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "création impossible"})
		return
	}
	writeJSON(w, http.StatusCreated, p)
}

// adminShopProductFromURL lit l’article de la route, ou répond l’erreur et renvoie nil.
func (h *Handlers) adminShopProductFromURL(w http.ResponseWriter, r *http.Request) *models.ShopProduct {
	oid, err := primitive.ObjectIDFromHex(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "identifiant invalide"})
		return nil
	}
	p, err := h.db.GetShopProduct(r.Context(), oid)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "article introuvable"})
		return nil
	}
	return p
}

func (h *Handlers) AdminPatchShopProduct(w http.ResponseWriter, r *http.Request) {
	p := h.adminShopProductFromURL(w, r)
	if p == nil {
		return
	}
	var b adminShopProductBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "json invalide"})
		return
	}
	if err := applyShopProductBody(p, &b); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := h.db.UpdateShopProduct(r.Context(), p); err != nil {
		if errors.Is(err, store.ErrShopSlugTaken) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "un autre article utilise déjà cette adresse"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "enregistrement impossible"})
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (h *Handlers) AdminDeleteShopProduct(w http.ResponseWriter, r *http.Request) {
	p := h.adminShopProductFromURL(w, r)
	if p == nil {
		return
	}
	if err := h.db.DeleteShopProduct(r.Context(), p.ID); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "suppression impossible"})
		return
	}
	for _, m := range p.Media {
		h.removeShopMediaFile(m.ID)
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h *Handlers) AdminReorderShopProducts(w http.ResponseWriter, r *http.Request) {
	var b struct {
		IDs []string `json:"ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "json invalide"})
		return
	}
	ids := make([]primitive.ObjectID, 0, len(b.IDs))
	for _, raw := range b.IDs {
		oid, err := primitive.ObjectIDFromHex(raw)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "identifiant invalide"})
			return
		}
		ids = append(ids, oid)
	}
	if err := h.db.ReorderShopProducts(r.Context(), ids); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "enregistrement impossible"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

/* —— Admin : réglages —— */

func (h *Handlers) AdminGetShopSettings(w http.ResponseWriter, r *http.Request) {
	s, err := h.db.GetShopSettings(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "lecture impossible"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"settings": s, "stripe_enabled": h.stripeEnabled()})
}

// validateShopSettings : ouvrir les ventes engage juridiquement, d’où les conditions obligatoires.
func validateShopSettings(s *models.ShopSettings) error {
	if s.ShippingCents < 0 {
		return errors.New("frais de livraison invalides")
	}
	countries := make([]string, 0, len(s.Countries))
	for _, c := range s.Countries {
		c = strings.ToUpper(strings.TrimSpace(c))
		if c == "" {
			continue
		}
		if !shopCountry.MatchString(c) {
			return errors.New("pays invalide : utilise un code à deux lettres, par exemple FR")
		}
		countries = append(countries, c)
	}
	s.Countries = countries
	if s.SalesOpen {
		if len(countries) == 0 {
			return errors.New("pour ouvrir les ventes : indique au moins un pays de livraison")
		}
		if strings.TrimSpace(s.Terms) == "" {
			return errors.New("pour ouvrir les ventes : renseigne les conditions générales de vente")
		}
	}
	return nil
}

func (h *Handlers) AdminPutShopSettings(w http.ResponseWriter, r *http.Request) {
	var s models.ShopSettings
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<18)).Decode(&s); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "json invalide"})
		return
	}
	if err := validateShopSettings(&s); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := h.db.UpsertShopSettings(r.Context(), s); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "enregistrement impossible"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"settings": s, "stripe_enabled": h.stripeEnabled()})
}

/* —— Admin : commandes —— */

func (h *Handlers) AdminListShopOrders(w http.ResponseWriter, r *http.Request) {
	h.sweepStaleShopOrders(r.Context())
	orders, err := h.db.ListShopOrders(r.Context(), strings.TrimSpace(r.URL.Query().Get("status")), 200)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "lecture impossible"})
		return
	}
	type row struct {
		models.ShopOrder
		Reference string `json:"reference"`
	}
	out := make([]row, 0, len(orders))
	for i := range orders {
		out = append(out, row{ShopOrder: orders[i], Reference: shopOrderReference(&orders[i])})
	}
	writeJSON(w, http.StatusOK, map[string]any{"orders": out})
}

// AdminPatchShopOrder : expédier une commande payée, ou l’annuler et remettre le stock en vente.
// Le remboursement se fait dans le tableau de bord Stripe, ce que l’écran rappelle.
func (h *Handlers) AdminPatchShopOrder(w http.ResponseWriter, r *http.Request) {
	oid, err := primitive.ObjectIDFromHex(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "identifiant invalide"})
		return
	}
	var b struct {
		Status   string `json:"status"`
		Tracking string `json:"tracking"`
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "json invalide"})
		return
	}
	ctx := r.Context()
	var won bool
	switch b.Status {
	case models.ShopOrderShipped:
		won, err = h.db.TransitionShopOrder(ctx, oid, models.ShopOrderPaid, models.ShopOrderShipped, bson.M{
			"shipped_at": time.Now().UTC(),
			"tracking":   strings.TrimSpace(b.Tracking),
		})
	case models.ShopOrderCanceled:
		won, err = h.db.TransitionShopOrder(ctx, oid, models.ShopOrderPaid, models.ShopOrderCanceled, nil)
		if err == nil && won {
			if order, gerr := h.db.GetShopOrder(ctx, oid); gerr == nil {
				h.releaseShopOrderStock(ctx, order.Items)
			}
		}
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "état invalide"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "enregistrement impossible"})
		return
	}
	if !won {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "seule une commande payée et non expédiée peut changer d’état"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
