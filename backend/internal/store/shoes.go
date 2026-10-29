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

func (d *DB) CreateShoe(ctx context.Context, s *models.Shoe) error {
	if s.ID.IsZero() {
		s.ID = primitive.NewObjectID()
	}
	if s.CreatedAt.IsZero() {
		s.CreatedAt = time.Now().UTC()
	}
	_, err := d.shoes.InsertOne(ctx, s)
	return err
}

// ListShoesByUser : paires actives d'abord, puis la plus récemment ajoutée.
func (d *DB) ListShoesByUser(ctx context.Context, userID primitive.ObjectID) ([]models.Shoe, error) {
	opts := options.Find().SetSort(bson.D{{Key: "created_at", Value: -1}})
	cur, err := d.shoes.Find(ctx, bson.M{"user_id": userID}, opts)
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	out := []models.Shoe{}
	for cur.Next(ctx) {
		var s models.Shoe
		if err := cur.Decode(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, cur.Err()
}

func (d *DB) GetShoeByUser(ctx context.Context, userID, shoeID primitive.ObjectID) (*models.Shoe, error) {
	var s models.Shoe
	err := d.shoes.FindOne(ctx, bson.M{"_id": shoeID, "user_id": userID}).Decode(&s)
	if err == mongo.ErrNoDocuments {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// UpdateShoe applique set / unset sur une paire du coureur.
func (d *DB) UpdateShoe(ctx context.Context, userID, shoeID primitive.ObjectID, set, unset bson.M) error {
	update := bson.M{}
	if len(set) > 0 {
		update["$set"] = set
	}
	if len(unset) > 0 {
		update["$unset"] = unset
	}
	if len(update) == 0 {
		return nil
	}
	res, err := d.shoes.UpdateOne(ctx, bson.M{"_id": shoeID, "user_id": userID}, update)
	if err != nil {
		return err
	}
	if res.MatchedCount == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteShoeByUser supprime la paire et la détache de ses courses : elles
// redeviennent des courses sans paire, sans revenir dans les questions.
func (d *DB) DeleteShoeByUser(ctx context.Context, userID, shoeID primitive.ObjectID) error {
	res, err := d.shoes.DeleteOne(ctx, bson.M{"_id": shoeID, "user_id": userID})
	if err != nil {
		return err
	}
	if res.DeletedCount == 0 {
		return ErrNotFound
	}
	_, err = d.liveRuns.UpdateMany(ctx,
		bson.M{"user_id": userID, "shoe_id": shoeID},
		bson.M{"$unset": bson.M{"shoe_id": ""}, "$set": bson.M{"shoe_skipped": true}},
	)
	return err
}

// ShoeRunTotals : distance et nombre de courses rattachées à chaque paire.
func (d *DB) ShoeRunTotals(ctx context.Context, userID primitive.ObjectID) (map[primitive.ObjectID]models.ShoeRunTotal, error) {
	cur, err := d.liveRuns.Aggregate(ctx, mongo.Pipeline{
		{{Key: "$match", Value: bson.M{"user_id": userID, "shoe_id": bson.M{"$type": "objectId"}}}},
		{{Key: "$group", Value: bson.M{
			"_id":        "$shoe_id",
			"distance_m": bson.M{"$sum": "$distance_m"},
			"runs":       bson.M{"$sum": 1},
		}}},
	})
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	out := map[primitive.ObjectID]models.ShoeRunTotal{}
	for cur.Next(ctx) {
		var t models.ShoeRunTotal
		if err := cur.Decode(&t); err != nil {
			return nil, err
		}
		out[t.ShoeID] = t
	}
	return out, cur.Err()
}

// SetLiveRunShoe rattache une course à une paire (shoeID non nil) ou note
// que le coureur n'en a choisi aucune (shoeID nil).
func (d *DB) SetLiveRunShoe(ctx context.Context, userID, runID primitive.ObjectID, shoeID *primitive.ObjectID) error {
	update := bson.M{"$set": bson.M{"shoe_skipped": true}, "$unset": bson.M{"shoe_id": ""}}
	if shoeID != nil {
		update = bson.M{"$set": bson.M{"shoe_id": *shoeID}, "$unset": bson.M{"shoe_skipped": ""}}
	}
	res, err := d.liveRuns.UpdateOne(ctx, bson.M{"_id": runID, "user_id": userID}, update)
	if err != nil {
		return err
	}
	if res.MatchedCount == 0 {
		return ErrNotFound
	}
	return nil
}

// PendingShoeRun : la course la plus récente, enregistrée après since, à
// laquelle aucune paire n'est rattachée et pour laquelle le coureur n'a pas
// encore répondu. ErrNotFound s'il n'y en a pas.
func (d *DB) PendingShoeRun(ctx context.Context, userID primitive.ObjectID, since time.Time) (*models.LiveRun, error) {
	opts := options.FindOne().
		SetSort(bson.D{{Key: "created_at", Value: -1}}).
		SetProjection(bson.M{"track_points": 0})
	var lr models.LiveRun
	err := d.liveRuns.FindOne(ctx, bson.M{
		"user_id":      userID,
		"created_at":   bson.M{"$gte": since.UTC()},
		"shoe_id":      bson.M{"$exists": false},
		"shoe_skipped": bson.M{"$ne": true},
	}, opts).Decode(&lr)
	if err == mongo.ErrNoDocuments {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &lr, nil
}

// CountPendingShoeRuns : nombre de courses encore sans réponse, pour dire
// « encore 2 courses » sous la question.
func (d *DB) CountPendingShoeRuns(ctx context.Context, userID primitive.ObjectID, since time.Time) (int64, error) {
	return d.liveRuns.CountDocuments(ctx, bson.M{
		"user_id":      userID,
		"created_at":   bson.M{"$gte": since.UTC()},
		"shoe_id":      bson.M{"$exists": false},
		"shoe_skipped": bson.M{"$ne": true},
	})
}

// ListLiveRunsSince : courses enregistrées depuis since, sans trace.
func (d *DB) ListLiveRunsSince(ctx context.Context, userID primitive.ObjectID, since time.Time) ([]models.LiveRun, error) {
	opts := options.Find().
		SetSort(bson.D{{Key: "created_at", Value: -1}}).
		SetProjection(bson.M{"track_points": 0, "splits": 0})
	cur, err := d.liveRuns.Find(ctx, bson.M{
		"user_id":    userID,
		"created_at": bson.M{"$gte": since.UTC()},
	}, opts)
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var out []models.LiveRun
	for cur.Next(ctx) {
		var lr models.LiveRun
		if err := cur.Decode(&lr); err != nil {
			return nil, err
		}
		out = append(out, lr)
	}
	return out, cur.Err()
}

// ListLiveRunsForGoal : courses lancées depuis une séance de cet objectif, sans trace.
func (d *DB) ListLiveRunsForGoal(ctx context.Context, userID, goalID primitive.ObjectID) ([]models.LiveRun, error) {
	opts := options.Find().SetProjection(bson.M{"track_points": 0, "splits": 0})
	cur, err := d.liveRuns.Find(ctx, bson.M{
		"user_id":              userID,
		"goal_session.goal_id": goalID,
	}, opts)
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var out []models.LiveRun
	for cur.Next(ctx) {
		var lr models.LiveRun
		if err := cur.Decode(&lr); err != nil {
			return nil, err
		}
		out = append(out, lr)
	}
	return out, cur.Err()
}
