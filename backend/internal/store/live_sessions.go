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

// liveSessionMaxAge : au-delà, une session jamais close n'est plus « en direct ».
// Large devant un ultra ou une longue sortie trail.
const liveSessionMaxAge = 8 * time.Hour

// StartLiveSession ouvre une session en direct et clôt celles que le coureur
// aurait laissées ouvertes : on ne court qu'une course à la fois.
func (d *DB) StartLiveSession(ctx context.Context, s *models.LiveSession) error {
	now := time.Now().UTC()
	if s.StartedAt.IsZero() {
		s.StartedAt = now
	}
	s.ID = primitive.NewObjectID()
	s.ExpiresAt = s.StartedAt.Add(liveSessionMaxAge)
	if err := d.EndOpenLiveSessions(ctx, s.UserID, time.Time{}); err != nil {
		return err
	}
	_, err := d.liveSessions.InsertOne(ctx, s)
	return err
}

// EndOpenLiveSessions clôt les sessions ouvertes d'un coureur parties au plus tard
// à `startedBefore` (zéro : toutes). La borne compte quand une vieille course de
// montre arrive pendant une nouvelle sortie : elle ne doit pas couper le direct.
func (d *DB) EndOpenLiveSessions(ctx context.Context, userID primitive.ObjectID, startedBefore time.Time) error {
	now := time.Now().UTC()
	filter := bson.M{"user_id": userID, "ended_at": bson.M{"$exists": false}}
	if !startedBefore.IsZero() {
		filter["started_at"] = bson.M{"$lte": startedBefore}
	}
	_, err := d.liveSessions.UpdateMany(ctx, filter, bson.M{"$set": bson.M{"ended_at": now}})
	return err
}

// EndLiveSession clôt une session du coureur. Déjà close ou inconnue : rien à faire.
func (d *DB) EndLiveSession(ctx context.Context, userID, sessionID primitive.ObjectID) error {
	now := time.Now().UTC()
	_, err := d.liveSessions.UpdateOne(ctx,
		bson.M{"_id": sessionID, "user_id": userID, "ended_at": bson.M{"$exists": false}},
		bson.M{"$set": bson.M{"ended_at": now}},
	)
	return err
}

func (d *DB) GetLiveSession(ctx context.Context, sessionID primitive.ObjectID) (*models.LiveSession, error) {
	var s models.LiveSession
	err := d.liveSessions.FindOne(ctx, bson.M{"_id": sessionID}).Decode(&s)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// LastLiveSessionStart : départ annoncé le plus récent d'un coureur, close ou
// non. Sert à ne pas renotifier les amis à chaque faux départ.
func (d *DB) LastLiveSessionStart(ctx context.Context, userID primitive.ObjectID) (time.Time, error) {
	var s models.LiveSession
	err := d.liveSessions.FindOne(ctx,
		bson.M{"user_id": userID},
		options.FindOne().SetSort(bson.D{{Key: "started_at", Value: -1}}).SetProjection(bson.M{"started_at": 1}),
	).Decode(&s)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return time.Time{}, nil
	}
	return s.StartedAt, err
}

// ListOpenLiveSessionsByUsers : courses en direct de ces comptes, la plus récente d'abord.
func (d *DB) ListOpenLiveSessionsByUsers(ctx context.Context, userIDs []primitive.ObjectID) ([]models.LiveSession, error) {
	if len(userIDs) == 0 {
		return nil, nil
	}
	cur, err := d.liveSessions.Find(ctx,
		bson.M{
			"user_id":    bson.M{"$in": userIDs},
			"ended_at":   bson.M{"$exists": false},
			"expires_at": bson.M{"$gt": time.Now().UTC()},
		},
		options.Find().SetSort(bson.D{{Key: "started_at", Value: -1}}),
	)
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var out []models.LiveSession
	if err := cur.All(ctx, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (d *DB) InsertLiveBoost(ctx context.Context, b *models.LiveBoost) error {
	b.ID = primitive.NewObjectID()
	if b.CreatedAt.IsZero() {
		b.CreatedAt = time.Now().UTC()
	}
	_, err := d.liveBoosts.InsertOne(ctx, b)
	return err
}

// LastLiveBoostFrom : dernier boost de cet ami sur cette session, zéro s'il n'y en a pas.
func (d *DB) LastLiveBoostFrom(ctx context.Context, sessionID, fromUserID primitive.ObjectID) (time.Time, error) {
	var b models.LiveBoost
	err := d.liveBoosts.FindOne(ctx,
		bson.M{"session_id": sessionID, "from_user_id": fromUserID},
		options.FindOne().SetSort(bson.D{{Key: "created_at", Value: -1}}),
	).Decode(&b)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return time.Time{}, nil
	}
	return b.CreatedAt, err
}

// ListLiveBoostsAfter : boosts d'une session postérieurs à `after`, du plus ancien
// au plus récent — l'ordre dans lequel le coureur doit les entendre.
func (d *DB) ListLiveBoostsAfter(ctx context.Context, sessionID primitive.ObjectID, after time.Time, limit int64) ([]models.LiveBoost, error) {
	cur, err := d.liveBoosts.Find(ctx,
		bson.M{"session_id": sessionID, "created_at": bson.M{"$gt": after}},
		options.Find().SetSort(bson.D{{Key: "created_at", Value: 1}}).SetLimit(limit),
	)
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var out []models.LiveBoost
	if err := cur.All(ctx, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// CountLiveBoostsBySessions : nombre de boosts reçus par session.
func (d *DB) CountLiveBoostsBySessions(ctx context.Context, sessionIDs []primitive.ObjectID) (map[primitive.ObjectID]int, error) {
	out := make(map[primitive.ObjectID]int, len(sessionIDs))
	if len(sessionIDs) == 0 {
		return out, nil
	}
	cur, err := d.liveBoosts.Aggregate(ctx, mongo.Pipeline{
		{{Key: "$match", Value: bson.M{"session_id": bson.M{"$in": sessionIDs}}}},
		{{Key: "$group", Value: bson.M{"_id": "$session_id", "n": bson.M{"$sum": 1}}}},
	})
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	for cur.Next(ctx) {
		var row struct {
			ID primitive.ObjectID `bson:"_id"`
			N  int                `bson:"n"`
		}
		if err := cur.Decode(&row); err == nil {
			out[row.ID] = row.N
		}
	}
	return out, cur.Err()
}
