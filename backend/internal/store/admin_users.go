package store

import (
	"context"
	"time"

	"runapp/internal/models"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo/options"
)

type UserListItem struct {
	ID           string    `json:"id"`
	Email        string    `json:"email"`
	Role         string    `json:"role"`
	Plan         string    `json:"plan"`
	StravaLinked bool      `json:"strava_linked"`
	CreatedAt    time.Time `json:"created_at"`
}

func (d *DB) ListUsers(ctx context.Context, skip, limit int64) ([]UserListItem, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	opts := options.Find().
		SetSort(bson.D{{Key: "created_at", Value: -1}}).
		SetSkip(skip).
		SetLimit(limit).
		SetProjection(bson.M{
			"email": 1, "created_at": 1, "role": 1, "plan": 1,
			"strava.access_token": 1,
		})
	cur, err := d.users.Find(ctx, bson.M{}, opts)
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var out []UserListItem
	for cur.Next(ctx) {
		var raw struct {
			ID        primitive.ObjectID `bson:"_id"`
			Email     string             `bson:"email"`
			Role      string             `bson:"role"`
			Plan      string             `bson:"plan"`
			CreatedAt time.Time          `bson:"created_at"`
			Strava    *struct {
				AccessToken string `bson:"access_token"`
			} `bson:"strava,omitempty"`
		}
		if err := cur.Decode(&raw); err != nil {
			return nil, err
		}
		linked := raw.Strava != nil && raw.Strava.AccessToken != ""
		u := models.User{Role: raw.Role, Plan: raw.Plan}
		out = append(out, UserListItem{
			ID:           raw.ID.Hex(),
			Email:        raw.Email,
			Role:         u.EffectiveRole(),
			Plan:         u.EffectivePlan(),
			StravaLinked: linked,
			CreatedAt:    raw.CreatedAt,
		})
	}
	if out == nil {
		out = []UserListItem{}
	}
	return out, cur.Err()
}

func (d *DB) CountUsers(ctx context.Context) (int64, error) {
	return d.users.CountDocuments(ctx, bson.M{})
}

func (d *DB) CountUsersByPlan(ctx context.Context, plan string) (int64, error) {
	return d.users.CountDocuments(ctx, bson.M{"plan": plan})
}

// CountUsersStandard : plan absent, vide ou explicitement standard.
func (d *DB) CountUsersStandard(ctx context.Context) (int64, error) {
	return d.users.CountDocuments(ctx, bson.M{
		"$or": []bson.M{
			{"plan": bson.M{"$in": bson.A{"", models.PlanStandard}}},
			{"plan": bson.M{"$exists": false}},
		},
	})
}

func (d *DB) CountUsersSince(ctx context.Context, since time.Time) (int64, error) {
	return d.users.CountDocuments(ctx, bson.M{"created_at": bson.M{"$gte": since}})
}

func (d *DB) UpdateUserRolePlan(ctx context.Context, id primitive.ObjectID, role, plan *string) error {
	set := bson.M{}
	if role != nil {
		set["role"] = *role
	}
	if plan != nil {
		set["plan"] = *plan
	}
	if len(set) == 0 {
		return nil
	}
	res, err := d.users.UpdateOne(ctx, bson.M{"_id": id}, bson.M{"$set": set})
	if err != nil {
		return err
	}
	if res.MatchedCount == 0 {
		return ErrNotFound
	}
	return nil
}

func (d *DB) UpdateUserPlan(ctx context.Context, id primitive.ObjectID, plan string) error {
	return d.UpdateUserRolePlan(ctx, id, nil, &plan)
}

func (d *DB) CountGoalsByUser(ctx context.Context, userID primitive.ObjectID) (int64, error) {
	return d.goals.CountDocuments(ctx, bson.M{"user_id": userID})
}

func (d *DB) CountLiveRunsForUser(ctx context.Context, userID primitive.ObjectID) (int64, error) {
	return d.liveRuns.CountDocuments(ctx, bson.M{"user_id": userID})
}

func (d *DB) ListGoalsSummariesByUser(ctx context.Context, userID primitive.ObjectID, limit int) ([]bson.M, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	opts := options.Find().
		SetSort(bson.D{{Key: "created_at", Value: -1}}).
		SetLimit(int64(limit)).
		SetProjection(bson.M{
			"_id": 1, "created_at": 1, "distance_km": 1, "distance_label": 1, "weeks": 1,
		})
	cur, err := d.goals.Find(ctx, bson.M{"user_id": userID}, opts)
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var out []bson.M
	for cur.Next(ctx) {
		var m bson.M
		if err := cur.Decode(&m); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, cur.Err()
}
