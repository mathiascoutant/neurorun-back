package store

import (
	"context"
	"errors"

	"runapp/internal/models"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

const offerConfigKey = "offer_config"

func (d *DB) GetOfferConfig(ctx context.Context) (models.OfferConfig, error) {
	var doc struct {
		Value models.OfferConfig `bson:"value"`
	}
	err := d.settings.FindOne(ctx, bson.M{"_id": offerConfigKey}).Decode(&doc)
	if errors.Is(err, mongo.ErrNoDocuments) {
		c := models.DefaultOfferConfig()
		return c, nil
	}
	if err != nil {
		return models.OfferConfig{}, err
	}
	doc.Value.MergeDefaults()
	return doc.Value, nil
}

func (d *DB) UpsertOfferConfig(ctx context.Context, cfg models.OfferConfig) error {
	cfg.MergeDefaults()
	_, err := d.settings.UpdateOne(ctx,
		bson.M{"_id": offerConfigKey},
		bson.M{"$set": bson.M{"value": cfg}},
		options.Update().SetUpsert(true),
	)
	return err
}
