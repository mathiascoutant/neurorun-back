package models

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

type StravaTokens struct {
	AccessToken  string    `bson:"access_token" json:"-"`
	RefreshToken string    `bson:"refresh_token" json:"-"`
	ExpiresAt    time.Time `bson:"expires_at" json:"-"`
}

const (
	RoleUser  = "user"
	RoleAdmin = "admin"

	PlanStandard    = "standard"
	PlanStrava      = "strava"
	PlanPerformance = "performance"
)

// Genres persistés côté API (inscription / profil).
const (
	GenderFemale      = "female"
	GenderMale        = "male"
	GenderOther       = "other"
	GenderUnspecified = "unspecified"
)

// Fournisseurs de paiement d’un abonnement. Stripe encaisse le web ; Apple encaisse l’app iOS, où
// la règle App Store 3.1.1 impose l’achat intégré pour du contenu numérique consommé dans l’app.
const (
	BillingProviderStripe = "stripe"
	BillingProviderApple  = "apple"
)

// Billing : état de l’abonnement. Jamais exposé en JSON (userPublic ne renvoie que le plan effectif).
type Billing struct {
	// Provider : qui encaisse (stripe | apple). Vide sur les enregistrements antérieurs à l’ajout
	// de l’IAP — EffectiveProvider() les traite alors comme Stripe.
	Provider             string `bson:"provider,omitempty" json:"-"`
	StripeCustomerID     string `bson:"stripe_customer_id,omitempty" json:"-"`
	StripeSubscriptionID string `bson:"stripe_subscription_id,omitempty" json:"-"`
	// AppleOriginalTransactionID : identifiant stable d’un abonnement iOS, conservé à travers tous
	// les renouvellements. C’est la clé de rattachement d’une notification App Store à un compte.
	AppleOriginalTransactionID string `bson:"apple_original_transaction_id,omitempty" json:"-"`
	AppleProductID             string `bson:"apple_product_id,omitempty" json:"-"`
	// AppleEnvironment : Sandbox | Production. Un achat sandbox ne doit jamais ouvrir l’offre en prod.
	AppleEnvironment string `bson:"apple_environment,omitempty" json:"-"`
	// Status : statut brut du fournisseur (Stripe : incomplete, active, past_due, canceled… ;
	// Apple : active, expired, billing_retry, grace_period, revoked).
	Status string `bson:"status,omitempty" json:"-"`
	// Plan couvert par l’abonnement en cours (strava | performance).
	Plan              string     `bson:"plan,omitempty" json:"-"`
	AmountCents       int64      `bson:"amount_cents,omitempty" json:"-"`
	CurrentPeriodEnd  *time.Time `bson:"current_period_end,omitempty" json:"-"`
	CancelAtPeriodEnd bool       `bson:"cancel_at_period_end,omitempty" json:"-"`
	UpdatedAt         time.Time  `bson:"updated_at,omitempty" json:"-"`
}

// EffectiveProvider : les abonnements créés avant l’ajout de l’IAP n’ont pas de champ `provider`
// et sont tous des abonnements Stripe.
func (b *Billing) EffectiveProvider() string {
	if b == nil {
		return ""
	}
	if b.Provider == BillingProviderApple {
		return BillingProviderApple
	}
	return BillingProviderStripe
}

type User struct {
	ID           primitive.ObjectID `bson:"_id,omitempty" json:"id"`
	Email        string             `bson:"email" json:"email"`
	PasswordHash string             `bson:"password_hash" json:"-"`
	FirstName    string             `bson:"first_name,omitempty" json:"first_name,omitempty"`
	LastName     string             `bson:"last_name,omitempty" json:"last_name,omitempty"`
	// BirthDate : date seule, format RFC YYYY-MM-DD (UTC).
	BirthDate string        `bson:"birth_date,omitempty" json:"birth_date,omitempty"`
	Gender    string        `bson:"gender,omitempty" json:"gender,omitempty"`
	Strava    *StravaTokens `bson:"strava,omitempty" json:"-"`
	Role      string        `bson:"role,omitempty" json:"role"`
	Plan      string        `bson:"plan,omitempty" json:"plan"`
	Billing   *Billing      `bson:"billing,omitempty" json:"-"`
	CreatedAt time.Time     `bson:"created_at" json:"created_at"`
	// LastSeenAt : dernière activité sur l’API (connexion ou requête authentifiée récente).
	LastSeenAt *time.Time `bson:"last_seen_at,omitempty" json:"last_seen_at,omitempty"`
}

func (u *User) EffectiveRole() string {
	if u.Role == RoleAdmin {
		return RoleAdmin
	}
	return RoleUser
}

func (u *User) EffectivePlan() string {
	if u.Plan == "" {
		return PlanStandard
	}
	if u.Plan == PlanStrava || u.Plan == PlanPerformance {
		return u.Plan
	}
	return PlanStandard
}

func (u *User) HasStrava() bool {
	return u.Strava != nil && u.Strava.AccessToken != ""
}
