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

// LeaderboardTotal : cumul d'un coureur sur une période, courses comptées seulement.
type LeaderboardTotal struct {
	UserID primitive.ObjectID `bson:"_id"`
	Points float64            `bson:"points"`
	Km     float64            `bson:"km"`
	Runs   int                `bson:"runs"`
	// NoteKm / NotedKm : somme de note × km et km des courses notées. Leur rapport
	// donne la note moyenne pondérée par les kilomètres.
	NoteKm    float64 `bson:"note_km"`
	NotedKm   float64 `bson:"noted_km"`
	LongestKm float64 `bson:"longest_km"`
}

// AvgNote : note moyenne pondérée par les km ; nil sans course notée.
func (t LeaderboardTotal) AvgNote() *float64 {
	if t.NotedKm <= 0 {
		return nil
	}
	v := t.NoteKm / t.NotedKm
	return &v
}

func leaderboardMatch(since *time.Time, userID *primitive.ObjectID) bson.M {
	m := bson.M{"leaderboard.counted": true}
	if since != nil {
		m["leaderboard.at"] = bson.M{"$gte": since.UTC()}
	}
	if userID != nil {
		m["user_id"] = *userID
	}
	return m
}

var leaderboardGroup = bson.M{
	"_id":    "$user_id",
	"points": bson.M{"$sum": "$leaderboard.points"},
	"km":     bson.M{"$sum": "$leaderboard.km"},
	"runs":   bson.M{"$sum": 1},
	"note_km": bson.M{"$sum": bson.M{"$cond": bson.A{
		bson.M{"$gt": bson.A{"$leaderboard.note", nil}},
		bson.M{"$multiply": bson.A{"$leaderboard.note", "$leaderboard.km"}},
		0,
	}}},
	"noted_km": bson.M{"$sum": bson.M{"$cond": bson.A{
		bson.M{"$gt": bson.A{"$leaderboard.note", nil}},
		"$leaderboard.km",
		0,
	}}},
	"longest_km": bson.M{"$max": "$leaderboard.km"},
}

// LeaderboardTotals : cumul de chaque coureur depuis `since` (nil = depuis toujours).
func (d *DB) LeaderboardTotals(ctx context.Context, since *time.Time) ([]LeaderboardTotal, error) {
	cur, err := d.liveRuns.Aggregate(ctx, mongo.Pipeline{
		{{Key: "$match", Value: leaderboardMatch(since, nil)}},
		{{Key: "$group", Value: leaderboardGroup}},
	})
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var out []LeaderboardTotal
	if err := cur.All(ctx, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// LeaderboardTotalForUser : cumul d'un seul coureur ; zéro s'il n'a rien couru.
func (d *DB) LeaderboardTotalForUser(ctx context.Context, userID primitive.ObjectID, since *time.Time) (LeaderboardTotal, error) {
	cur, err := d.liveRuns.Aggregate(ctx, mongo.Pipeline{
		{{Key: "$match", Value: leaderboardMatch(since, &userID)}},
		{{Key: "$group", Value: leaderboardGroup}},
	})
	if err != nil {
		return LeaderboardTotal{}, err
	}
	defer cur.Close(ctx)
	var rows []LeaderboardTotal
	if err := cur.All(ctx, &rows); err != nil {
		return LeaderboardTotal{}, err
	}
	if len(rows) == 0 {
		return LeaderboardTotal{UserID: userID}, nil
	}
	return rows[0], nil
}

// LeaderboardUsers : ce que le classement affiche d'un compte. Ni e-mail ni date de
// naissance : la liste est visible de tous.
func (d *DB) LeaderboardUsers(ctx context.Context, ids []primitive.ObjectID) (map[primitive.ObjectID]models.User, error) {
	out := make(map[primitive.ObjectID]models.User, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	cur, err := d.users.Find(ctx,
		bson.M{"_id": bson.M{"$in": ids}},
		options.Find().SetProjection(bson.M{
			"_id": 1, "first_name": 1, "last_name": 1, "country": 1, "leaderboard_hidden": 1,
		}),
	)
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var rows []models.User
	if err := cur.All(ctx, &rows); err != nil {
		return nil, err
	}
	for i := range rows {
		out[rows[i].ID] = rows[i]
	}
	return out, nil
}

// LiveRunsWithoutLeaderboard : courses enregistrées avant les classements, avec
// seulement ce qu'il faut pour calculer leurs points.
func (d *DB) LiveRunsWithoutLeaderboard(ctx context.Context, limit int64) ([]models.LiveRun, error) {
	cur, err := d.liveRuns.Find(ctx,
		bson.M{"leaderboard": bson.M{"$exists": false}},
		options.Find().SetLimit(limit).SetProjection(bson.M{
			"_id": 1, "user_id": 1, "created_at": 1, "distance_m": 1, "moving_sec": 1,
			"avg_pace_sec_per_km": 1, "max_implied_speed_kmh": 1, "gps_start_ts_ms": 1,
			"score.scorable": 1, "score.total": 1,
		}),
	)
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var rows []models.LiveRun
	if err := cur.All(ctx, &rows); err != nil {
		return nil, err
	}
	return rows, nil
}

// SetLiveRunLeaderboard : n'écrit que si la course n'a pas déjà ses points, pour
// qu'un rattrapage ne remplace jamais des points figés.
func (d *DB) SetLiveRunLeaderboard(ctx context.Context, runID primitive.ObjectID, lb *models.LiveRunLeaderboard) error {
	_, err := d.liveRuns.UpdateOne(ctx,
		bson.M{"_id": runID, "leaderboard": bson.M{"$exists": false}},
		bson.M{"$set": bson.M{"leaderboard": lb}},
	)
	return err
}
