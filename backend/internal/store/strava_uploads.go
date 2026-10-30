package store

import (
	"context"
	"time"

	"runapp/internal/models"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// stravaUploadStale : un envoi resté « en cours » plus longtemps a été interrompu
// (redémarrage du serveur) et peut être repris.
const stravaUploadStale = 5 * time.Minute

// ClaimLiveRunStravaUpload passe la course à « en cours d'envoi » si personne ne
// s'en occupe déjà : jamais tentée, en échec, ou envoi abandonné. Faux sinon — la
// course part déjà, ou elle est déjà sur Strava. C'est ce verrou en base qui empêche
// l'envoi automatique et un « réessayer » simultané de créer deux activités.
func (d *DB) ClaimLiveRunStravaUpload(ctx context.Context, userID, runID primitive.ObjectID, now time.Time) (bool, error) {
	filter := bson.M{
		"_id":     runID,
		"user_id": userID,
		"$or": bson.A{
			bson.M{"strava": bson.M{"$exists": false}},
			bson.M{"strava.status": models.StravaUploadFailed},
			bson.M{"strava.status": models.StravaUploadPending, "strava.updated_at": bson.M{"$lt": now.Add(-stravaUploadStale)}},
		},
	}
	res, err := d.liveRuns.UpdateOne(ctx, filter, bson.M{"$set": bson.M{
		"strava": models.LiveRunStrava{Status: models.StravaUploadPending, UpdatedAt: now},
	}})
	if err != nil {
		return false, err
	}
	return res.MatchedCount == 1, nil
}

// SetLiveRunStrava enregistre l'issue de l'envoi.
func (d *DB) SetLiveRunStrava(ctx context.Context, runID primitive.ObjectID, s models.LiveRunStrava) error {
	_, err := d.liveRuns.UpdateOne(ctx, bson.M{"_id": runID}, bson.M{"$set": bson.M{"strava": s}})
	return err
}
