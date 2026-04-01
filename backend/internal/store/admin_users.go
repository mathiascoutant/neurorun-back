package store

import (
	"context"
	"sort"
	"time"

	"runapp/internal/models"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
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

// DeleteUserCascade supprime l’utilisateur et ses données liées (conversations, objectifs, courses live).
func (d *DB) DeleteUserCascade(ctx context.Context, id primitive.ObjectID) error {
	if _, err := d.conversations.DeleteMany(ctx, bson.M{"user_id": id}); err != nil {
		return err
	}
	if _, err := d.goals.DeleteMany(ctx, bson.M{"user_id": id}); err != nil {
		return err
	}
	if _, err := d.liveRuns.DeleteMany(ctx, bson.M{"user_id": id}); err != nil {
		return err
	}
	res, err := d.users.DeleteOne(ctx, bson.M{"_id": id})
	if err != nil {
		return err
	}
	if res.DeletedCount == 0 {
		return ErrNotFound
	}
	return nil
}

// SignupDay : inscriptions agrégées par jour (UTC).
type SignupDay struct {
	Day   string `json:"day"`
	Count int64  `json:"count"`
}

// SignupsByDayUTC retourne les `days` derniers jours (y compris jours à 0 inscription).
func (d *DB) SignupsByDayUTC(ctx context.Context, days int) ([]SignupDay, error) {
	if days <= 0 || days > 90 {
		days = 30
	}
	now := time.Now().UTC()
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, -(days - 1))
	pipeline := mongo.Pipeline{
		{{Key: "$match", Value: bson.M{"created_at": bson.M{"$gte": start}}}},
		{{Key: "$group", Value: bson.M{
			"_id": bson.M{"$dateToString": bson.M{"format": "%Y-%m-%d", "date": "$created_at", "timezone": "UTC"}},
			"count": bson.M{"$sum": 1},
		}}},
		{{Key: "$sort", Value: bson.M{"_id": 1}}},
	}
	cur, err := d.users.Aggregate(ctx, pipeline)
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	byDay := map[string]int64{}
	for cur.Next(ctx) {
		var row struct {
			ID    string `bson:"_id"`
			Count int64  `bson:"count"`
		}
		if err := cur.Decode(&row); err != nil {
			return nil, err
		}
		byDay[row.ID] = row.Count
	}
	if err := cur.Err(); err != nil {
		return nil, err
	}
	out := make([]SignupDay, 0, days)
	for i := 0; i < days; i++ {
		dt := start.AddDate(0, 0, i)
		day := dt.Format("2006-01-02")
		out = append(out, SignupDay{Day: day, Count: byDay[day]})
	}
	return out, nil
}

// TopUserActivity : score = courses live + objectifs + conversations (approx. « activité »).
type TopUserActivity struct {
	UserID       string `json:"user_id"`
	Email        string `json:"email"`
	Activity     int64  `json:"activity"`
	LiveRuns     int64  `json:"live_runs"`
	Goals        int64  `json:"goals"`
	Conversations int64  `json:"conversations"`
}

func aggregateUserCounts(ctx context.Context, coll *mongo.Collection) (map[primitive.ObjectID]int64, error) {
	cur, err := coll.Aggregate(ctx, mongo.Pipeline{
		{{Key: "$group", Value: bson.M{"_id": "$user_id", "c": bson.M{"$sum": 1}}}},
	})
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	out := map[primitive.ObjectID]int64{}
	for cur.Next(ctx) {
		var row struct {
			ID primitive.ObjectID `bson:"_id"`
			C  int64              `bson:"c"`
		}
		if err := cur.Decode(&row); err != nil {
			return nil, err
		}
		out[row.ID] = row.C
	}
	return out, cur.Err()
}

// TopUsersByActivity classe les utilisateurs par (live_runs + goals + conversations).
func (d *DB) TopUsersByActivity(ctx context.Context, limit int) ([]TopUserActivity, error) {
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	lr, err := aggregateUserCounts(ctx, d.liveRuns)
	if err != nil {
		return nil, err
	}
	gc, err := aggregateUserCounts(ctx, d.goals)
	if err != nil {
		return nil, err
	}
	cc, err := aggregateUserCounts(ctx, d.conversations)
	if err != nil {
		return nil, err
	}
	score := map[primitive.ObjectID]int64{}
	add := func(m map[primitive.ObjectID]int64) {
		for id, n := range m {
			score[id] += n
		}
	}
	add(lr)
	add(gc)
	add(cc)
	type pair struct {
		id    primitive.ObjectID
		score int64
	}
	var pairs []pair
	for id, s := range score {
		pairs = append(pairs, pair{id: id, score: s})
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].score != pairs[j].score {
			return pairs[i].score > pairs[j].score
		}
		return pairs[i].id.Hex() < pairs[j].id.Hex()
	})
	if len(pairs) > limit {
		pairs = pairs[:limit]
	}
	if len(pairs) == 0 {
		return []TopUserActivity{}, nil
	}
	ids := make([]primitive.ObjectID, len(pairs))
	for i := range pairs {
		ids[i] = pairs[i].id
	}
	cur, err := d.users.Find(ctx, bson.M{"_id": bson.M{"$in": ids}}, options.Find().SetProjection(bson.M{"email": 1}))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	emailOf := map[primitive.ObjectID]string{}
	for cur.Next(ctx) {
		var u struct {
			ID    primitive.ObjectID `bson:"_id"`
			Email string             `bson:"email"`
		}
		if err := cur.Decode(&u); err != nil {
			return nil, err
		}
		emailOf[u.ID] = u.Email
	}
	if err := cur.Err(); err != nil {
		return nil, err
	}
	out := make([]TopUserActivity, 0, len(pairs))
	for _, p := range pairs {
		out = append(out, TopUserActivity{
			UserID:        p.id.Hex(),
			Email:         emailOf[p.id],
			Activity:      p.score,
			LiveRuns:      lr[p.id],
			Goals:         gc[p.id],
			Conversations: cc[p.id],
		})
	}
	return out, nil
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
