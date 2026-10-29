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

// ErrSuggestionExists : une suggestion existe déjà pour cet objectif ce jour-là.
var ErrSuggestionExists = errors.New("suggestion déjà faite")

// InsertCoachSuggestion n'en pose qu'une par objectif et par jour : l'index
// unique tranche entre deux passes concurrentes, et seule celle qui insère
// envoie la notification.
func (d *DB) InsertCoachSuggestion(ctx context.Context, s *models.CoachSuggestion) error {
	s.ID = primitive.NewObjectID()
	if s.CreatedAt.IsZero() {
		s.CreatedAt = time.Now().UTC()
	}
	if s.Status == "" {
		s.Status = models.CoachSuggestionPending
	}
	_, err := d.coachSuggestions.InsertOne(ctx, s)
	if mongo.IsDuplicateKeyError(err) {
		return ErrSuggestionExists
	}
	return err
}

// CoachSuggestionFor : la suggestion de ce jour pour cet objectif, nil s'il n'y en a pas.
func (d *DB) CoachSuggestionFor(ctx context.Context, userID, goalID primitive.ObjectID, day string) (*models.CoachSuggestion, error) {
	var s models.CoachSuggestion
	err := d.coachSuggestions.FindOne(ctx, bson.M{"user_id": userID, "goal_id": goalID, "day": day}).Decode(&s)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func (d *DB) GetCoachSuggestion(ctx context.Context, userID, id primitive.ObjectID) (*models.CoachSuggestion, error) {
	var s models.CoachSuggestion
	err := d.coachSuggestions.FindOne(ctx, bson.M{"_id": id, "user_id": userID}).Decode(&s)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// AnswerCoachSuggestion ne répond qu'à une suggestion encore en attente : deux
// appuis rapprochés ne déplacent pas deux fois la séance.
func (d *DB) AnswerCoachSuggestion(ctx context.Context, userID, id primitive.ObjectID, status string) (bool, error) {
	now := time.Now().UTC()
	res, err := d.coachSuggestions.UpdateOne(ctx,
		bson.M{"_id": id, "user_id": userID, "status": models.CoachSuggestionPending},
		bson.M{"$set": bson.M{"status": status, "answered_at": now}},
	)
	if err != nil {
		return false, err
	}
	return res.ModifiedCount == 1, nil
}

// RevertCoachSuggestion remet en attente une suggestion dont l'application a échoué.
func (d *DB) RevertCoachSuggestion(ctx context.Context, userID, id primitive.ObjectID) error {
	_, err := d.coachSuggestions.UpdateOne(ctx,
		bson.M{"_id": id, "user_id": userID},
		bson.M{"$set": bson.M{"status": models.CoachSuggestionPending}, "$unset": bson.M{"answered_at": ""}},
	)
	return err
}

// ListGoalsForSchedule : objectifs créés depuis `since`, sans le texte du plan ni
// le fil du coach — de quoi résoudre le calendrier de chacun, rien de plus.
func (d *DB) ListGoalsForSchedule(ctx context.Context, since time.Time) ([]models.Goal, error) {
	cur, err := d.goals.Find(ctx,
		bson.M{"created_at": bson.M{"$gte": since}},
		options.Find().SetProjection(bson.M{"plan": 0, "coach_thread": 0}),
	)
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var out []models.Goal
	if err := cur.All(ctx, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// LastRunStartPoint : départ de la course GPS la plus récente depuis `since`.
// C'est là que le coureur court d'habitude, et donc la météo qui le concerne.
func (d *DB) LastRunStartPoint(ctx context.Context, userID primitive.ObjectID, since time.Time) (lat, lng float64, ok bool, err error) {
	var lr models.LiveRun
	err = d.liveRuns.FindOne(ctx,
		bson.M{"user_id": userID, "created_at": bson.M{"$gte": since}, "track_points.0": bson.M{"$exists": true}},
		options.FindOne().
			SetSort(bson.D{{Key: "created_at", Value: -1}}).
			SetProjection(bson.M{"track_points": bson.M{"$slice": 1}}),
	).Decode(&lr)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return 0, 0, false, nil
	}
	if err != nil || len(lr.TrackPoints) == 0 {
		return 0, 0, false, err
	}
	return lr.TrackPoints[0].Lat, lr.TrackPoints[0].Lng, true, nil
}
