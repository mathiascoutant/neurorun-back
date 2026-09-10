package store

import (
	"context"
	"time"

	"runapp/internal/models"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

func (d *DB) CreateVmaTest(ctx context.Context, t *models.VmaTest) error {
	if t.ID.IsZero() {
		t.ID = primitive.NewObjectID()
	}
	if t.CreatedAt.IsZero() {
		t.CreatedAt = time.Now().UTC()
	}
	_, err := d.vmaTests.InsertOne(ctx, t)
	if mongo.IsDuplicateKeyError(err) {
		return ErrDuplicateVmaTest
	}
	return err
}

// FindVmaTestByClientID retourne le test déjà enregistré pour ce
// client_test_id, ou ErrNotFound.
func (d *DB) FindVmaTestByClientID(
	ctx context.Context, userID primitive.ObjectID, clientTestID string,
) (*models.VmaTest, error) {
	var t models.VmaTest
	err := d.vmaTests.FindOne(ctx, bson.M{
		"user_id":        userID,
		"client_test_id": clientTestID,
	}).Decode(&t)
	if err == mongo.ErrNoDocuments {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// LatestVmaTest retourne le dernier test passé, qui fait foi pour la VMA
// courante. ErrNotFound si l'utilisateur n'a jamais testé.
func (d *DB) LatestVmaTest(ctx context.Context, userID primitive.ObjectID) (*models.VmaTest, error) {
	var t models.VmaTest
	opts := options.FindOne().SetSort(bson.D{{Key: "created_at", Value: -1}})
	err := d.vmaTests.FindOne(ctx, bson.M{"user_id": userID}, opts).Decode(&t)
	if err == mongo.ErrNoDocuments {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func (d *DB) ListVmaTestsByUser(
	ctx context.Context, userID primitive.ObjectID, limit int,
) ([]models.VmaTest, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	opts := options.Find().
		SetSort(bson.D{{Key: "created_at", Value: -1}}).
		SetLimit(int64(limit)).
		SetProjection(bson.M{"track_points": 0})
	cur, err := d.vmaTests.Find(ctx, bson.M{"user_id": userID}, opts)
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var out []models.VmaTest
	for cur.Next(ctx) {
		var t models.VmaTest
		if err := cur.Decode(&t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, cur.Err()
}

func (d *DB) DeleteVmaTest(ctx context.Context, userID, testID primitive.ObjectID) error {
	res, err := d.vmaTests.DeleteOne(ctx, bson.M{"_id": testID, "user_id": userID})
	if err != nil {
		return err
	}
	if res.DeletedCount == 0 {
		return ErrNotFound
	}
	return nil
}

// SetLiveRunScore écrit la note complète d'une course. Sert au rattrapage des
// courses enregistrées avant la fonctionnalité, ou avant le premier test de VMA.
func (d *DB) SetLiveRunScore(
	ctx context.Context, userID, runID primitive.ObjectID, score *models.RunScore,
) error {
	res, err := d.liveRuns.UpdateOne(ctx,
		bson.M{"_id": runID, "user_id": userID},
		bson.M{"$set": bson.M{"score": score}},
	)
	if err != nil {
		return err
	}
	if res.MatchedCount == 0 {
		return ErrNotFound
	}
	return nil
}

// SetLiveRunScoreExplanation mémorise l'explication rédigée pour une note.
// Elle n'est calculée qu'une fois, à la première consultation de la course.
func (d *DB) SetLiveRunScoreExplanation(
	ctx context.Context, userID, runID primitive.ObjectID, exp *models.RunScoreExplanation,
) error {
	res, err := d.liveRuns.UpdateOne(ctx,
		bson.M{"_id": runID, "user_id": userID},
		bson.M{"$set": bson.M{"score.explanation": exp}},
	)
	if err != nil {
		return err
	}
	if res.MatchedCount == 0 {
		return ErrNotFound
	}
	return nil
}
