package store

import (
	"context"
	"errors"
	"strings"
	"time"

	"runapp/internal/models"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

func normalizePromoCode(s string) string {
	return strings.ToUpper(strings.TrimSpace(s))
}

func (d *DB) CreatePromoCode(ctx context.Context, p *models.PromoCode) error {
	if p.ID.IsZero() {
		p.ID = primitive.NewObjectID()
	}
	p.Code = normalizePromoCode(p.Code)
	if p.CreatedAt.IsZero() {
		p.CreatedAt = time.Now().UTC()
	}
	_, err := d.promoCodes.InsertOne(ctx, p)
	if mongo.IsDuplicateKeyError(err) {
		return ErrDuplicatePromo
	}
	return err
}

func (d *DB) ListPromoCodes(ctx context.Context) ([]models.PromoCode, error) {
	opts := options.Find().SetSort(bson.D{{Key: "created_at", Value: -1}})
	cur, err := d.promoCodes.Find(ctx, bson.M{}, opts)
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var out []models.PromoCode
	for cur.Next(ctx) {
		var p models.PromoCode
		if err := cur.Decode(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, cur.Err()
}

func (d *DB) DeletePromoCode(ctx context.Context, id primitive.ObjectID) error {
	res, err := d.promoCodes.DeleteOne(ctx, bson.M{"_id": id})
	if err != nil {
		return err
	}
	if res.DeletedCount == 0 {
		return ErrNotFound
	}
	return nil
}

func (d *DB) UpdatePromoCode(ctx context.Context, id primitive.ObjectID, patch bson.M) error {
	if len(patch) == 0 {
		return nil
	}
	res, err := d.promoCodes.UpdateOne(ctx, bson.M{"_id": id}, bson.M{"$set": patch})
	if err != nil {
		return err
	}
	if res.MatchedCount == 0 {
		return ErrNotFound
	}
	return nil
}

func (d *DB) FindPromoByID(ctx context.Context, id primitive.ObjectID) (*models.PromoCode, error) {
	var p models.PromoCode
	err := d.promoCodes.FindOne(ctx, bson.M{"_id": id}).Decode(&p)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// FindPromoByCode retourne le document promo ou ErrNotFound.
func (d *DB) FindPromoByCode(ctx context.Context, code string) (*models.PromoCode, error) {
	code = normalizePromoCode(code)
	var p models.PromoCode
	err := d.promoCodes.FindOne(ctx, bson.M{"code": code}).Decode(&p)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// IncrementPromoUse incrémente atomiquement si quotas et dates OK.
func (d *DB) IncrementPromoUse(ctx context.Context, id primitive.ObjectID) error {
	now := time.Now().UTC()
	expiryOK := []bson.M{
		{"expires_at": bson.M{"$exists": false}},
		{"expires_at": nil},
		{"expires_at": bson.M{"$gt": now}},
	}
	quotaOK := []bson.M{
		{"max_uses": 0},
		{"$expr": bson.M{"$lt": []interface{}{"$uses", "$max_uses"}}},
	}
	filter := bson.M{
		"_id":    id,
		"active": true,
		"$and": []bson.M{
			{"$or": expiryOK},
			{"$or": quotaOK},
		},
	}
	res, err := d.promoCodes.UpdateOne(ctx, filter, bson.M{"$inc": bson.M{"uses": 1}})
	if err != nil {
		return err
	}
	if res.MatchedCount == 0 {
		return errors.New("promo invalide ou quota atteint")
	}
	return nil
}
