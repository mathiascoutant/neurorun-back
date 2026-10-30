package store

import (
	"context"
	"time"

	"runapp/internal/models"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// ListLiveRunsForRecords : toutes les courses NeuroRun du coureur, sans leur
// trace (seul le premier point, pour situer le départ). Les records portent sur
// tout l'historique : la liste ordinaire, bornée et complète, n'y suffirait pas.
func (d *DB) ListLiveRunsForRecords(ctx context.Context, userID primitive.ObjectID) ([]models.LiveRun, error) {
	opts := options.Find().
		SetSort(bson.D{{Key: "created_at", Value: -1}}).
		SetProjection(bson.M{
			"created_at":      1,
			"name":            1,
			"distance_m":      1,
			"moving_sec":      1,
			"wall_sec":        1,
			"gps_start_ts_ms": 1,
			"gps_end_ts_ms":   1,
			"splits":          1,
			"strava":          1,
			"track_points":    bson.M{"$slice": 1},
		})
	cur, err := d.liveRuns.Find(ctx, bson.M{"user_id": userID}, opts)
	if err != nil {
		return nil, err
	}
	var out []models.LiveRun
	if err := cur.All(ctx, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// StravaBestEffortsByUser : portions Strava déjà lues, par activité.
func (d *DB) StravaBestEffortsByUser(ctx context.Context, userID primitive.ObjectID) (map[int64]models.StravaBestEfforts, error) {
	cur, err := d.stravaBestEfforts.Find(ctx, bson.M{"user_id": userID})
	if err != nil {
		return nil, err
	}
	var rows []models.StravaBestEfforts
	if err := cur.All(ctx, &rows); err != nil {
		return nil, err
	}
	out := make(map[int64]models.StravaBestEfforts, len(rows))
	for _, r := range rows {
		out[r.StravaID] = r
	}
	return out, nil
}

// SaveStravaBestEfforts enregistre les portions lues d'une activité.
func (d *DB) SaveStravaBestEfforts(ctx context.Context, userID primitive.ObjectID, stravaID int64, efforts map[string]models.StravaEffort, version int) error {
	if efforts == nil {
		efforts = map[string]models.StravaEffort{}
	}
	_, err := d.stravaBestEfforts.UpdateOne(ctx,
		bson.M{"user_id": userID, "strava_id": stravaID},
		bson.M{
			"$set":         bson.M{"efforts": efforts, "fetched_at": time.Now().UTC(), "v": version},
			"$setOnInsert": bson.M{"_id": primitive.NewObjectID()},
		},
		options.Update().SetUpsert(true),
	)
	return err
}
