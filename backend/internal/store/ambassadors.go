package store

import (
	"context"
	"errors"
	"time"

	"runapp/internal/models"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// CreateAmbassador : un compte ne peut être ambassadeur qu’une fois — l’index unique sur
// user_id ferme la course entre deux créations simultanées.
func (d *DB) CreateAmbassador(ctx context.Context, a *models.Ambassador) error {
	if a.ID.IsZero() {
		a.ID = primitive.NewObjectID()
	}
	now := time.Now().UTC()
	if a.CreatedAt.IsZero() {
		a.CreatedAt = now
	}
	a.UpdatedAt = now
	if a.Status == "" {
		a.Status = models.AmbassadorStatusPending
	}
	_, err := d.ambassadors.InsertOne(ctx, a)
	if mongo.IsDuplicateKeyError(err) {
		return ErrDuplicateAmbassador
	}
	return err
}

func (d *DB) ListAmbassadors(ctx context.Context) ([]models.Ambassador, error) {
	opts := options.Find().SetSort(bson.D{{Key: "created_at", Value: -1}})
	cur, err := d.ambassadors.Find(ctx, bson.M{}, opts)
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	out := []models.Ambassador{}
	for cur.Next(ctx) {
		var a models.Ambassador
		if err := cur.Decode(&a); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, cur.Err()
}

func (d *DB) findAmbassador(ctx context.Context, filter bson.M) (*models.Ambassador, error) {
	var a models.Ambassador
	err := d.ambassadors.FindOne(ctx, filter).Decode(&a)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &a, nil
}

func (d *DB) FindAmbassadorByID(ctx context.Context, id primitive.ObjectID) (*models.Ambassador, error) {
	return d.findAmbassador(ctx, bson.M{"_id": id})
}

func (d *DB) FindAmbassadorByUserID(ctx context.Context, userID primitive.ObjectID) (*models.Ambassador, error) {
	return d.findAmbassador(ctx, bson.M{"user_id": userID})
}

// FindAmbassadorByPromoID : sert à refuser la suppression d’un code promo rattaché à un contrat.
func (d *DB) FindAmbassadorByPromoID(ctx context.Context, promoID primitive.ObjectID) (*models.Ambassador, error) {
	return d.findAmbassador(ctx, bson.M{"promo_code_id": promoID})
}

func (d *DB) UpdateAmbassador(ctx context.Context, id primitive.ObjectID, patch bson.M) error {
	if len(patch) == 0 {
		return nil
	}
	patch["updated_at"] = time.Now().UTC()
	res, err := d.ambassadors.UpdateOne(ctx, bson.M{"_id": id}, bson.M{"$set": patch})
	if err != nil {
		return err
	}
	if res.MatchedCount == 0 {
		return ErrNotFound
	}
	return nil
}

// SignAmbassadorContract : enregistre la signature de l’ambassadeur et l’identité qu’il a
// saisie, mais seulement s’il n’a pas déjà signé — un contrat signé ne se réécrit pas.
func (d *DB) SignAmbassadorContract(
	ctx context.Context,
	id primitive.ObjectID,
	lastName, firstName, instagram, version string,
	sig models.Signature,
) error {
	filter := bson.M{
		"_id": id,
		"$or": []bson.M{
			{"ambassador_signature": bson.M{"$exists": false}},
			{"ambassador_signature": nil},
		},
		"status": bson.M{"$ne": models.AmbassadorStatusEnded},
	}
	res, err := d.ambassadors.UpdateOne(ctx, filter, bson.M{"$set": bson.M{
		"contract_last_name":   lastName,
		"contract_first_name":  firstName,
		"instagram":            instagram,
		"contract_version":     version,
		"ambassador_signature": sig,
		"status":               models.AmbassadorStatusSigned,
		"updated_at":           time.Now().UTC(),
	}})
	if err != nil {
		return err
	}
	if res.MatchedCount == 0 {
		return ErrNotFound
	}
	return nil
}

// CountersignAmbassadorContract : signature NeuroRun, qui met le contrat en vigueur. Exige
// que l’ambassadeur ait signé d’abord, et refuse une seconde contre-signature.
func (d *DB) CountersignAmbassadorContract(
	ctx context.Context,
	id primitive.ObjectID,
	sig models.Signature,
) error {
	filter := bson.M{
		"_id":                  id,
		"ambassador_signature": bson.M{"$ne": nil, "$exists": true},
		"$or": []bson.M{
			{"company_signature": bson.M{"$exists": false}},
			{"company_signature": nil},
		},
		"status": bson.M{"$ne": models.AmbassadorStatusEnded},
	}
	res, err := d.ambassadors.UpdateOne(ctx, filter, bson.M{"$set": bson.M{
		"company_signature": sig,
		"status":            models.AmbassadorStatusActive,
		"updated_at":        time.Now().UTC(),
	}})
	if err != nil {
		return err
	}
	if res.MatchedCount == 0 {
		return ErrNotFound
	}
	return nil
}

func (d *DB) DeleteAmbassador(ctx context.Context, id primitive.ObjectID) error {
	res, err := d.ambassadors.DeleteOne(ctx, bson.M{"_id": id})
	if err != nil {
		return err
	}
	if res.DeletedCount == 0 {
		return ErrNotFound
	}
	return nil
}

func (d *DB) CountAmbassadors(ctx context.Context) (int64, error) {
	return d.ambassadors.CountDocuments(ctx, bson.M{})
}
