package models

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// Natures de remise d’un code promo.
const (
	// PromoKindPercent : pourcentage retiré du prix de l’offre.
	PromoKindPercent = "percent"
	// PromoKindAmount : montant fixe en euros déduit du prix de l’offre.
	PromoKindAmount = "amount"
)

// PromoAmountOffMaxCents : garde-fou de saisie (1 000 €). Un montant plus élevé qu’un
// abonnement mensuel est forcément une faute de frappe — autant la refuser à la création.
const PromoAmountOffMaxCents int64 = 100_000

// PromoCode : réduction appliquée au prix de l’abonnement, en pourcentage ou en euros.
type PromoCode struct {
	ID   primitive.ObjectID `bson:"_id,omitempty" json:"id,omitempty"`
	Code string             `bson:"code" json:"code"`
	// Kind : percent | amount. Vide sur les codes créés avant les remises en euros ;
	// EffectiveKind les lit alors comme des pourcentages.
	Kind string `bson:"kind,omitempty" json:"kind,omitempty"`
	// PercentOff : 0–100, utilisé quand Kind vaut percent.
	PercentOff int `bson:"percent_off" json:"percent_off"`
	// AmountOffCents : remise en centimes, utilisée quand Kind vaut amount. En centimes
	// parce que c’est un montant encaissé : un float accumulerait des arrondis.
	AmountOffCents  int64      `bson:"amount_off_cents,omitempty" json:"amount_off_cents,omitempty"`
	MaxUses         int        `bson:"max_uses" json:"max_uses"` // 0 = illimité
	Uses            int        `bson:"uses" json:"uses"`
	ExpiresAt       *time.Time `bson:"expires_at,omitempty" json:"expires_at,omitempty"`
	Active          bool       `bson:"active" json:"active"`
	ApplicablePlans []string   `bson:"applicable_plans,omitempty" json:"applicable_plans,omitempty"` // vide = tous
	CreatedAt       time.Time  `bson:"created_at" json:"created_at"`
}

// NormalizePromoKind ramène une valeur reçue d’un client à percent ou amount.
func NormalizePromoKind(kind string) string {
	if strings.TrimSpace(strings.ToLower(kind)) == PromoKindAmount {
		return PromoKindAmount
	}
	return PromoKindPercent
}

func (p *PromoCode) EffectiveKind() string {
	if p == nil {
		return PromoKindPercent
	}
	return NormalizePromoKind(p.Kind)
}

// ValidateDiscount : une remise doit retirer quelque chose, et rester dans ses bornes.
func (p *PromoCode) ValidateDiscount() error {
	if p == nil {
		return errors.New("code promo manquant")
	}
	if p.EffectiveKind() == PromoKindAmount {
		if p.AmountOffCents <= 0 {
			return errors.New("montant de remise requis")
		}
		if p.AmountOffCents > PromoAmountOffMaxCents {
			return errors.New("montant de remise trop élevé")
		}
		return nil
	}
	if p.PercentOff <= 0 || p.PercentOff > 100 {
		return errors.New("pourcentage de remise invalide (1–100)")
	}
	return nil
}

// roundCents : arrondi au centime, plancher à zéro — un prix ne descend jamais sous 0 €.
func roundCents(v float64) float64 {
	if !(v > 0) {
		return 0
	}
	return math.Round(v*100) / 100
}

// DiscountedPriceEUR : prix à payer après remise.
func (p *PromoCode) DiscountedPriceEUR(baseEUR float64) float64 {
	base := roundCents(baseEUR)
	if p == nil || base == 0 {
		return base
	}
	if p.EffectiveKind() == PromoKindAmount {
		if p.AmountOffCents <= 0 {
			return base
		}
		return roundCents(base - float64(p.AmountOffCents)/100)
	}
	if p.PercentOff <= 0 {
		return base
	}
	if p.PercentOff >= 100 {
		return 0
	}
	return roundCents(base * float64(100-p.PercentOff) / 100)
}

// DiscountEUR : ce que la remise fait économiser sur ce prix.
func (p *PromoCode) DiscountEUR(baseEUR float64) float64 {
	return roundCents(roundCents(baseEUR) - p.DiscountedPriceEUR(baseEUR))
}

// AmountOffEUR : remise en euros d’un code en montant fixe (0 pour un pourcentage).
func (p *PromoCode) AmountOffEUR() float64 {
	if p == nil || p.EffectiveKind() != PromoKindAmount {
		return 0
	}
	return roundCents(float64(p.AmountOffCents) / 100)
}

// DiscountLabel : « 30 % » ou « 5 € », tel qu’on l’écrit dans un contrat ou une facture.
func (p *PromoCode) DiscountLabel() string {
	if p == nil {
		return ""
	}
	if p.EffectiveKind() == PromoKindAmount {
		return FormatEUR(p.AmountOffEUR())
	}
	return fmt.Sprintf("%d %%", p.PercentOff)
}

// Exhausted : quota d’utilisations atteint (un code épuisé reste actif en base).
func (p *PromoCode) Exhausted() bool {
	return p != nil && p.MaxUses > 0 && p.Uses >= p.MaxUses
}

// Usable : utilisable à l’instant t — actif, non expiré, quota disponible.
func (p *PromoCode) Usable() bool {
	if p == nil || !p.Active || p.Exhausted() {
		return false
	}
	return p.ExpiresAt == nil || time.Now().UTC().Before(*p.ExpiresAt)
}

// FormatEUR : montant en euros à la française — « 5 € », « 4,50 € ».
func FormatEUR(v float64) string {
	s := fmt.Sprintf("%.2f", v)
	s = strings.TrimSuffix(s, ".00")
	return strings.ReplaceAll(s, ".", ",") + " €"
}
