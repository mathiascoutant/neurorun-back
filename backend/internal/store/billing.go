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
func (d *DB) SetUserBilling(ctx context.Context, id primitive.ObjectID, b models.Billing) error {
	b.UpdatedAt = time.Now().UTC()
	set := bson.M{
		"billing.stripe_subscription_id": b.StripeSubscriptionID,
		"billing.status":                 b.Status,
		"billing.plan":                   b.Plan,
		"billing.amount_cents":           b.AmountCents,
		"billing.cancel_at_period_end":   b.CancelAtPeriodEnd,
		"billing.updated_at":             b.UpdatedAt,
	}
	if b.StripeCustomerID != "" {
		set["billing.stripe_customer_id"] = b.StripeCustomerID
	}
	update := bson.M{"$set": set}
	if b.CurrentPeriodEnd != nil {
		set["billing.current_period_end"] = *b.CurrentPeriodEnd
	} else {
		update["$unset"] = bson.M{"billing.current_period_end": ""}
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
