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

// ListLiveRunsForWrapped : courses enregistrées entre from (inclus) et to
// (exclu), sans la trace. Le récap de l'année lit des centaines de courses :
// seuls les chiffres utiles sont projetés, et de la trace on ne garde que le
// premier point, qui situe le départ (nombre de lieux différents).
func (d *DB) ListLiveRunsForWrapped(ctx context.Context, userID primitive.ObjectID, from, to time.Time) ([]models.LiveRun, error) {
	opts := options.Find().
		SetSort(bson.D{{Key: "created_at", Value: 1}}).
		SetProjection(bson.M{
			"_id":                           1,
			"created_at":                    1,
			"name":                          1,
			"session_type":                  1,
			"shoe_id":                       1,
			"distance_m":                    1,
			"moving_sec":                    1,
			"wall_sec":                      1,
			"gps_start_ts_ms":               1,
			"avg_pace_sec_per_km":           1,
			"client_stats.elevation_gain_m": 1,
			"score.total":                   1,
			"score.scorable":                1,
			"track_points":                  bson.M{"$slice": 1},
		})
	cur, err := d.liveRuns.Find(ctx, bson.M{
		"user_id":    userID,
		"created_at": bson.M{"$gte": from.UTC(), "$lt": to.UTC()},
	}, opts)
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var out []models.LiveRun
	if err := cur.All(ctx, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// WrappedBoosts : réactions échangées sur une période.
type WrappedBoosts struct {
	// Received : réactions reçues sur ses courses, par auteur (soi exclu).
	Received map[primitive.ObjectID]int
	// Sent : réactions posées sur les courses des autres.
	Sent int
}

// BoostStatsBetween compte les réactions reçues (par ami) et envoyées entre
// from et to, d'après leur date de première pose.
func (d *DB) BoostStatsBetween(ctx context.Context, userID primitive.ObjectID, from, to time.Time) (WrappedBoosts, error) {
	out := WrappedBoosts{Received: map[primitive.ObjectID]int{}}
	period := bson.M{"$gte": from.UTC(), "$lt": to.UTC()}

	cur, err := d.boosts.Aggregate(ctx, mongo.Pipeline{
		{{Key: "$match", Value: bson.M{
			"run_owner_id": userID,
			"from_user_id": bson.M{"$ne": userID},
			"created_at":   period,
		}}},
		{{Key: "$group", Value: bson.M{"_id": "$from_user_id", "n": bson.M{"$sum": 1}}}},
	})
	if err != nil {
		return out, err
	}
	defer cur.Close(ctx)
	for cur.Next(ctx) {
		var row struct {
			ID primitive.ObjectID `bson:"_id"`
			N  int                `bson:"n"`
		}
		if err := cur.Decode(&row); err != nil {
			return out, err
		}
		out.Received[row.ID] = row.N
	}
	if err := cur.Err(); err != nil {
		return out, err
	}

	sent, err := d.boosts.CountDocuments(ctx, bson.M{
		"from_user_id": userID,
		"run_owner_id": bson.M{"$ne": userID},
		"created_at":   period,
	})
	if err != nil {
		return out, err
	}
	out.Sent = int(sent)
	return out, nil
}
