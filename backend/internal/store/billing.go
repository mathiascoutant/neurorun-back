package store

import (
	"context"
	"time"

	"runapp/internal/models"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
)

// SetUserStripeCustomer mémorise le client Stripe (créé une seule fois par compte).
func (d *DB) SetUserStripeCustomer(ctx context.Context, id primitive.ObjectID, customerID string) error {
	res, err := d.users.UpdateOne(ctx, bson.M{"_id": id}, bson.M{"$set": bson.M{
		"billing.stripe_customer_id": customerID,
		"billing.updated_at":         time.Now().UTC(),
	}})
	if err != nil {
		return err
	}
	if res.MatchedCount == 0 {
		return ErrNotFound
	}
	return nil
}

// SetUserBilling remplace l’état d’abonnement en conservant le client Stripe existant.
//
// Un compte n’a qu’un abonnement à la fois : on efface donc la référence du fournisseur qui n’est
// pas celui de `b`, sinon l’espace facturation continuerait d’afficher l’abonnement précédent.
// Le client Stripe, lui, survit à un passage sur Apple : il reste réutilisable si l’utilisateur
// revient payer par le web.
func (d *DB) SetUserBilling(ctx context.Context, id primitive.ObjectID, b models.Billing) error {
	b.UpdatedAt = time.Now().UTC()
	provider := b.EffectiveProvider()
	set := bson.M{
		"billing.provider":             provider,
		"billing.status":               b.Status,
		"billing.plan":                 b.Plan,
		"billing.amount_cents":         b.AmountCents,
		"billing.cancel_at_period_end": b.CancelAtPeriodEnd,
		"billing.updated_at":           b.UpdatedAt,
	}
	unset := bson.M{}

	if provider == models.BillingProviderApple {
		set["billing.apple_original_transaction_id"] = b.AppleOriginalTransactionID
		set["billing.apple_product_id"] = b.AppleProductID
		set["billing.apple_environment"] = b.AppleEnvironment
		unset["billing.stripe_subscription_id"] = ""
	} else {
		set["billing.stripe_subscription_id"] = b.StripeSubscriptionID
		unset["billing.apple_original_transaction_id"] = ""
		unset["billing.apple_product_id"] = ""
		unset["billing.apple_environment"] = ""
	}
	if b.StripeCustomerID != "" {
		set["billing.stripe_customer_id"] = b.StripeCustomerID
	}

	update := bson.M{"$set": set}
	if b.CurrentPeriodEnd != nil {
		set["billing.current_period_end"] = *b.CurrentPeriodEnd
	} else {
		unset["billing.current_period_end"] = ""
	}
	if len(unset) > 0 {
		update["$unset"] = unset
	}
	res, err := d.users.UpdateOne(ctx, bson.M{"_id": id}, update)
	if err != nil {
		return err
	}
	if res.MatchedCount == 0 {
		return ErrNotFound
	}
	return nil
}

// FindUserByAppleOriginalTransactionID sert au webhook App Store : les notifications ne portent que
// l’identifiant de transaction d’origine, jamais l’identité du compte NeuroRun.
func (d *DB) FindUserByAppleOriginalTransactionID(ctx context.Context, originalTxID string) (*models.User, error) {
	var u models.User
	err := d.users.FindOne(ctx, bson.M{"billing.apple_original_transaction_id": originalTxID}).Decode(&u)
	if err == mongo.ErrNoDocuments {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// FindUserByStripeCustomer sert au webhook : les événements Stripe ne portent que l’id client.
func (d *DB) FindUserByStripeCustomer(ctx context.Context, customerID string) (*models.User, error) {
	var u models.User
	err := d.users.FindOne(ctx, bson.M{"billing.stripe_customer_id": customerID}).Decode(&u)
	if err == mongo.ErrNoDocuments {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
}
