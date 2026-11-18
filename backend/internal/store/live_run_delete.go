package store

import (
	"context"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// DeleteLiveRun efface une course du coureur et les réactions qu'elle a reçues.
// Le reste (classement, records, chaussures, séance d'objectif) se calcule depuis
// live_runs et suit de lui-même. ErrNotFound si la course n'est pas à lui.
func (d *DB) DeleteLiveRun(ctx context.Context, userID, runID primitive.ObjectID) error {
	res, err := d.liveRuns.DeleteOne(ctx, bson.M{"_id": runID, "user_id": userID})
	if err != nil {
		return err
	}
	if res.DeletedCount == 0 {
		return ErrNotFound
	}
	_, err = d.boosts.DeleteMany(ctx, bson.M{"run_id": runID})
	return err
}

// HideStravaActivity : la copie Strava d'une course supprimée disparaît de NeuroRun
// (historique, tableau de bord, chaussures, records), Strava refusant de l'effacer.
func (d *DB) HideStravaActivity(ctx context.Context, userID primitive.ObjectID, activityID int64) error {
	if _, err := d.users.UpdateOne(ctx,
		bson.M{"_id": userID},
		bson.M{"$addToSet": bson.M{"hidden_strava_ids": activityID}},
	); err != nil {
		return err
	}
	filter := bson.M{"user_id": userID, "strava_id": activityID}
	if _, err := d.stravaRuns.DeleteMany(ctx, filter); err != nil {
		return err
	}
	_, err := d.stravaBestEfforts.DeleteMany(ctx, filter)
	return err
}
