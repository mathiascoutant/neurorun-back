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

// UpsertStravaRun enregistre une sortie Strava la première fois qu'on la voit et
// met à jour ses chiffres ensuite (renommée, distance corrigée sur Strava), sans
// jamais toucher à la paire déjà choisie.
func (d *DB) UpsertStravaRun(ctx context.Context, r *models.StravaRun) error {
	set := bson.M{
		"started_at": r.StartedAt.UTC(),
		"name":       r.Name,
		"distance_m": r.DistanceM,
		"moving_sec": r.MovingSec,
		"interval":   r.Interval,
	}
	if r.ElevGainM > 0 {
		set["elevation_gain_m"] = r.ElevGainM
	}
	if r.Duplicate {
		// Une copie reconnue l'est pour de bon : la course NeuroRun l'emporte.
		set["duplicate"] = true
	}
	_, err := d.stravaRuns.UpdateOne(ctx,
		bson.M{"user_id": r.UserID, "strava_id": r.StravaID},
		bson.M{
			"$set":         set,
			"$setOnInsert": bson.M{"_id": primitive.NewObjectID(), "imported_at": time.Now().UTC()},
		},
		options.Update().SetUpsert(true),
	)
	return err
}

func pendingStravaFilter(userID primitive.ObjectID, since time.Time) bson.M {
	return bson.M{
		"user_id":      userID,
		"started_at":   bson.M{"$gte": since.UTC()},
		"shoe_id":      bson.M{"$exists": false},
		"shoe_skipped": bson.M{"$ne": true},
		"duplicate":    bson.M{"$ne": true},
	}
}

// PendingStravaShoeRun : la sortie Strava la plus récente encore sans paire ni réponse.
func (d *DB) PendingStravaShoeRun(ctx context.Context, userID primitive.ObjectID, since time.Time) (*models.StravaRun, error) {
	var r models.StravaRun
	err := d.stravaRuns.FindOne(ctx, pendingStravaFilter(userID, since),
		options.FindOne().SetSort(bson.D{{Key: "started_at", Value: -1}}),
	).Decode(&r)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

func (d *DB) CountPendingStravaShoeRuns(ctx context.Context, userID primitive.ObjectID, since time.Time) (int64, error) {
	return d.stravaRuns.CountDocuments(ctx, pendingStravaFilter(userID, since))
}

// SetStravaRunShoe : même contrat que SetLiveRunShoe, pour une sortie Strava.
func (d *DB) SetStravaRunShoe(ctx context.Context, userID, runID primitive.ObjectID, shoeID *primitive.ObjectID) error {
	update := bson.M{"$set": bson.M{"shoe_skipped": true}, "$unset": bson.M{"shoe_id": ""}}
	if shoeID != nil {
		update = bson.M{"$set": bson.M{"shoe_id": *shoeID}, "$unset": bson.M{"shoe_skipped": ""}}
	}
	res, err := d.stravaRuns.UpdateOne(ctx, bson.M{"_id": runID, "user_id": userID}, update)
	if err != nil {
		return err
	}
	if res.MatchedCount == 0 {
		return ErrNotFound
	}
	return nil
}

// ListStravaRunsSince : sorties Strava retenues depuis `since`, la plus récente d'abord.
func (d *DB) ListStravaRunsSince(ctx context.Context, userID primitive.ObjectID, since time.Time) ([]models.StravaRun, error) {
	cur, err := d.stravaRuns.Find(ctx,
		bson.M{"user_id": userID, "started_at": bson.M{"$gte": since.UTC()}},
		options.Find().SetSort(bson.D{{Key: "started_at", Value: -1}}),
	)
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var out []models.StravaRun
	if err := cur.All(ctx, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// MarkStravaRunDuplicate : la course NeuroRun est arrivée après sa copie Strava ;
// la copie cesse d'être demandée et comptée.
func (d *DB) MarkStravaRunDuplicate(ctx context.Context, userID, stravaRunID primitive.ObjectID) error {
	_, err := d.stravaRuns.UpdateOne(ctx,
		bson.M{"_id": stravaRunID, "user_id": userID},
		bson.M{"$set": bson.M{"duplicate": true}},
	)
	return err
}

// stravaShoeTotals : la part Strava de ShoeRunTotals.
func (d *DB) stravaShoeTotals(ctx context.Context, userID primitive.ObjectID, into map[primitive.ObjectID]models.ShoeRunTotal) error {
	cur, err := d.stravaRuns.Aggregate(ctx, mongo.Pipeline{
		{{Key: "$match", Value: bson.M{"user_id": userID, "shoe_id": bson.M{"$type": "objectId"}, "duplicate": bson.M{"$ne": true}}}},
		{{Key: "$group", Value: bson.M{
			"_id":        "$shoe_id",
			"distance_m": bson.M{"$sum": "$distance_m"},
			"runs":       bson.M{"$sum": 1},
		}}},
	})
	if err != nil {
		return err
	}
	defer cur.Close(ctx)
	for cur.Next(ctx) {
		var t models.ShoeRunTotal
		if err := cur.Decode(&t); err != nil {
			return err
		}
		prev := into[t.ShoeID]
		prev.ShoeID = t.ShoeID
		prev.DistanceM += t.DistanceM
		prev.Runs += t.Runs
		into[t.ShoeID] = prev
	}
	return cur.Err()
}
