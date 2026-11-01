package models

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// Médias d’un article : des images, et une vidéo si l’administrateur le souhaite.
const (
	ShopMediaImage = "image"
	ShopMediaVideo = "video"
)

// États d’une commande. Le stock est réservé dès `pending` et rendu en passant à
// `expired` ou `canceled`.
const (
	ShopOrderPending  = "pending"
	ShopOrderPaid     = "paid"
	ShopOrderShipped  = "shipped"
	ShopOrderExpired  = "expired"
	ShopOrderCanceled = "canceled"
)

// ShopMedia : fichier d’un article, dans l’ordre d’affichage du tableau `Media`.
type ShopMedia struct {
	// ID : nom du fichier sur le disque (32 caractères hexadécimaux + extension).
	ID          string `bson:"id" json:"id"`
	Kind        string `bson:"kind" json:"kind"`
	ContentType string `bson:"content_type" json:"content_type"`
}

// ShopSize : une taille et ce qu’il en reste.
type ShopSize struct {
	Label string `bson:"label" json:"label"`
	Stock int    `bson:"stock" json:"stock"`
}

// ShopProduct : un article de la boutique, entièrement piloté depuis l’admin.
type ShopProduct struct {
	ID          primitive.ObjectID `bson:"_id,omitempty" json:"id"`
	Slug        string             `bson:"slug" json:"slug"`
	Name        string             `bson:"name" json:"name"`
	Description string             `bson:"description" json:"description"`
	// Active : publié. Un article en brouillon n’existe pas pour le public.
	Active bool `bson:"active" json:"active"`
	// Position : place dans le catalogue, du plus petit au plus grand.
	Position   int   `bson:"position" json:"position"`
	PriceCents int64 `bson:"price_cents" json:"price_cents"`
	// PromoPriceCents : prix remisé ; 0 = pas de promotion.
	PromoPriceCents int64 `bson:"promo_price_cents" json:"promo_price_cents"`
	// PromoEndsAt : fin de la promotion ; absent = jusqu’à ce qu’on la retire.
	PromoEndsAt *time.Time  `bson:"promo_ends_at,omitempty" json:"promo_ends_at,omitempty"`
	Sizes       []ShopSize  `bson:"sizes" json:"sizes"`
	Media       []ShopMedia `bson:"media" json:"media"`
	CreatedAt   time.Time   `bson:"created_at" json:"created_at"`
	UpdatedAt   time.Time   `bson:"updated_at" json:"updated_at"`
}

// PromoActive : la promotion s’applique-t-elle à cet instant ?
func (p *ShopProduct) PromoActive(now time.Time) bool {
	if p.PromoPriceCents <= 0 || p.PromoPriceCents >= p.PriceCents {
		return false
	}
	return p.PromoEndsAt == nil || now.Before(*p.PromoEndsAt)
}

// CurrentPriceCents : le prix réellement facturé à cet instant.
func (p *ShopProduct) CurrentPriceCents(now time.Time) int64 {
	if p.PromoActive(now) {
		return p.PromoPriceCents
	}
	return p.PriceCents
}

// StockFor : stock restant d’une taille ; false si la taille n’existe pas.
func (p *ShopProduct) StockFor(label string) (int, bool) {
	for _, s := range p.Sizes {
		if s.Label == label {
			return s.Stock, true
		}
	}
	return 0, false
}

// ShopOrderItem : ligne de commande. Nom et prix sont figés : modifier l’article plus tard ne
// doit pas réécrire ce que le client a payé.
type ShopOrderItem struct {
	ProductID primitive.ObjectID `bson:"product_id" json:"product_id"`
	Name      string             `bson:"name" json:"name"`
	Size      string             `bson:"size" json:"size"`
	Quantity  int                `bson:"quantity" json:"quantity"`
	UnitCents int64              `bson:"unit_cents" json:"unit_cents"`
}

// ShopAddress : adresse de livraison saisie sur la page de paiement Stripe.
type ShopAddress struct {
	Name       string `bson:"name" json:"name"`
	Line1      string `bson:"line1" json:"line1"`
	Line2      string `bson:"line2,omitempty" json:"line2,omitempty"`
	PostalCode string `bson:"postal_code" json:"postal_code"`
	City       string `bson:"city" json:"city"`
	Country    string `bson:"country" json:"country"`
}

// ShopOrder : une commande, de la réservation du stock jusqu’à l’expédition.
type ShopOrder struct {
	ID              primitive.ObjectID `bson:"_id,omitempty" json:"id"`
	Status          string             `bson:"status" json:"status"`
	Items           []ShopOrderItem    `bson:"items" json:"items"`
	SubtotalCents   int64              `bson:"subtotal_cents" json:"subtotal_cents"`
	ShippingCents   int64              `bson:"shipping_cents" json:"shipping_cents"`
	TotalCents      int64              `bson:"total_cents" json:"total_cents"`
	StripeSessionID string             `bson:"stripe_session_id,omitempty" json:"stripe_session_id,omitempty"`
	Email           string             `bson:"email,omitempty" json:"email,omitempty"`
	Address         *ShopAddress       `bson:"address,omitempty" json:"address,omitempty"`
	Tracking        string             `bson:"tracking,omitempty" json:"tracking,omitempty"`
	// ExpiresAt : au-delà, une commande restée en attente rend son stock.
	ExpiresAt time.Time  `bson:"expires_at" json:"expires_at"`
	CreatedAt time.Time  `bson:"created_at" json:"created_at"`
	PaidAt    *time.Time `bson:"paid_at,omitempty" json:"paid_at,omitempty"`
	ShippedAt *time.Time `bson:"shipped_at,omitempty" json:"shipped_at,omitempty"`
}

// ShopSettings : réglages généraux de la boutique (document unique).
type ShopSettings struct {
	// SalesOpen : sans ce feu vert, le catalogue reste visible mais personne ne peut payer.
	SalesOpen     bool     `bson:"sales_open" json:"sales_open"`
	ShippingCents int64    `bson:"shipping_cents" json:"shipping_cents"`
	Countries     []string `bson:"countries" json:"countries"`
	// Terms : conditions générales de vente, affichées avant le paiement.
	Terms string `bson:"terms" json:"terms"`
}

// DefaultShopSettings : ventes fermées tant que l’administrateur ne les a pas ouvertes.
func DefaultShopSettings() ShopSettings {
	return ShopSettings{Countries: []string{"FR"}}
}
