package store

import (
	"context"
	"strings"
	"time"

	"runapp/internal/models"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// CircuitCenterForModels calcule le centroïde pour index géospatial.
func CircuitCenterForModels(pts []models.LatLng) models.GeoPoint {
	if len(pts) == 0 {
		return models.NewGeoPoint(0, 0)
	}
	var slat, slng float64
	for _, p := range pts {
		slat += p.Lat
		slng += p.Lng
	}
	n := float64(len(pts))
	return models.NewGeoPoint(slng/n, slat/n)
}

// InsertCircuit enregistre un circuit ; name doit déjà être validé côté handler.
func (d *DB) InsertCircuit(ctx context.Context, c *models.Circuit) error {
	if c.ID.IsZero() {
		c.ID = primitive.NewObjectID()
	}
	c.CreatedAt = time.Now().UTC()
	_, err := d.circuits.InsertOne(ctx, c)
	return err
}

// FindCircuitsNear renvoie les circuits dont le centre est dans radiusMeters du point.
func (d *DB) FindCircuitsNear(ctx context.Context, lat, lng float64, radiusMeters float64, limit int64) ([]models.Circuit, error) {
	if limit <= 0 || limit > 100 {
		limit = 40
	}
	opts := options.Find().SetLimit(limit).SetSort(bson.D{{Key: "created_at", Value: -1}})
	cur, err := d.circuits.Find(ctx, bson.M{
		"center": bson.M{
			"$nearSphere": bson.M{
				"$geometry": bson.M{
					"type":        "Point",
					"coordinates": []float64{lng, lat},
				},
				"$maxDistance": radiusMeters,
			},
		},
	}, opts)
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var out []models.Circuit
	if err := cur.All(ctx, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (d *DB) FindCircuitByID(ctx context.Context, id primitive.ObjectID) (*models.Circuit, error) {
	var c models.Circuit
	err := d.circuits.FindOne(ctx, bson.M{"_id": id}).Decode(&c)
	if err == mongo.ErrNoDocuments {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func (d *DB) UpdateCircuitName(ctx context.Context, id primitive.ObjectID, name string) error {
	res, err := d.circuits.UpdateOne(ctx, bson.M{"_id": id}, bson.M{"$set": bson.M{"name": name}})
	if err != nil {
		return err
	}
	if res.MatchedCount == 0 {
		return ErrNotFound
	}
	return nil
}

func (d *DB) DeleteCircuit(ctx context.Context, id primitive.ObjectID) error {
	res, err := d.circuits.DeleteOne(ctx, bson.M{"_id": id})
	if err != nil {
		return err
	}
	if res.DeletedCount == 0 {
		return ErrNotFound
	}
	_, _ = d.circuitTimes.DeleteMany(ctx, bson.M{"circuit_id": id})
	return nil
}

// InsertCircuitTime enregistre un temps ; pas de dédup automatique (admin peut nettoyer).
func (d *DB) InsertCircuitTime(ctx context.Context, t *models.CircuitTime) error {
	if t.ID.IsZero() {
		t.ID = primitive.NewObjectID()
	}
	t.CreatedAt = time.Now().UTC()
	_, err := d.circuitTimes.InsertOne(ctx, t)
	return err
}

// ListTopCircuitTimes : meilleur temps par utilisateur, triés du plus rapide au plus lent, max n entrées (dédoublonnage).
func (d *DB) ListTopCircuitTimes(ctx context.Context, circuitID primitive.ObjectID, n int) ([]models.CircuitTime, error) {
	if n <= 0 || n > 50 {
		n = 10
	}
	pipeline := mongo.Pipeline{
		{{Key: "$match", Value: bson.M{"circuit_id": circuitID}}},
		{{Key: "$sort", Value: bson.D{{Key: "duration_ms", Value: 1}}}},
		{{Key: "$group", Value: bson.M{
			"_id": "$user_id",
			"doc": bson.M{"$first": "$$ROOT"},
		}}},
		{{Key: "$replaceRoot", Value: bson.M{"newRoot": "$doc"}}},
		{{Key: "$sort", Value: bson.D{{Key: "duration_ms", Value: 1}}}},
		{{Key: "$limit", Value: int64(n)}},
	}
	cur, err := d.circuitTimes.Aggregate(ctx, pipeline)
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var out []models.CircuitTime
	if err := cur.All(ctx, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (d *DB) CountCircuitCompletions(ctx context.Context, circuitID primitive.ObjectID) (int64, error) {
	return d.circuitTimes.CountDocuments(ctx, bson.M{"circuit_id": circuitID})
}

func (d *DB) CountDistinctCircuitParticipants(ctx context.Context, circuitID primitive.ObjectID) (int64, error) {
	cur, err := d.circuitTimes.Aggregate(ctx, []bson.M{
		{"$match": bson.M{"circuit_id": circuitID}},
		{"$group": bson.M{"_id": "$user_id"}},
		{"$count": "n"},
	})
	if err != nil {
		return 0, err
	}
	defer cur.Close(ctx)
	if !cur.Next(ctx) {
		return 0, nil
	}
	var doc struct {
		N int64 `bson:"n"`
	}
	if err := cur.Decode(&doc); err != nil {
		return 0, err
	}
	return doc.N, nil
}

func (d *DB) DeleteCircuitTime(ctx context.Context, id primitive.ObjectID) error {
	res, err := d.circuitTimes.DeleteOne(ctx, bson.M{"_id": id})
	if err != nil {
		return err
	}
	if res.DeletedCount == 0 {
		return ErrNotFound
	}
	return nil
}

func (d *DB) FindCircuitTimeByID(ctx context.Context, id primitive.ObjectID) (*models.CircuitTime, error) {
	var t models.CircuitTime
	err := d.circuitTimes.FindOne(ctx, bson.M{"_id": id}).Decode(&t)
	if err == mongo.ErrNoDocuments {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// AdminListCircuits recherche par sous-chaîne insensible à la casse sur name.
func (d *DB) AdminListCircuits(ctx context.Context, query string, skip, limit int64) ([]models.Circuit, int64, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if skip < 0 {
		skip = 0
	}
	filter := bson.M{}
	q := strings.TrimSpace(query)
	if q != "" {
		filter["name"] = bson.M{"$regex": escapeRegex(q), "$options": "i"}
	}
	total, err := d.circuits.CountDocuments(ctx, filter)
	if err != nil {
		return nil, 0, err
	}
	opts := options.Find().SetSkip(skip).SetLimit(limit).SetSort(bson.D{{Key: "created_at", Value: -1}})
	cur, err := d.circuits.Find(ctx, filter, opts)
	if err != nil {
		return nil, 0, err
	}
	defer cur.Close(ctx)
	var out []models.Circuit
	if err := cur.All(ctx, &out); err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

func escapeRegex(s string) string {
	repl := strings.NewReplacer(
		`\`, `\\`,
		`.`, `\.`,
		`*`, `\*`,
		`+`, `\+`,
		`?`, `\?`,
		`^`, `\^`,
		`$`, `\$`,
		`(`, `\(`,
		`)`, `\)`,
		`[`, `\[`,
		`]`, `\]`,
		`{`, `\{`,
		`}`, `\}`,
		`|`, `\|`,
	)
	return repl.Replace(s)
}

// AdminListAllCircuitTimes tous les temps d’un circuit (tri du plus rapide au plus lent).
func (d *DB) AdminListAllCircuitTimes(ctx context.Context, circuitID primitive.ObjectID, skip, limit int64) ([]models.CircuitTime, int64, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	if skip < 0 {
		skip = 0
	}
	filter := bson.M{"circuit_id": circuitID}
	total, err := d.circuitTimes.CountDocuments(ctx, filter)
	if err != nil {
		return nil, 0, err
	}
	opts := options.Find().SetSkip(skip).SetLimit(limit).SetSort(bson.D{{Key: "duration_ms", Value: 1}})
	cur, err := d.circuitTimes.Find(ctx, filter, opts)
	if err != nil {
		return nil, 0, err
	}
	defer cur.Close(ctx)
	var out []models.CircuitTime
	if err := cur.All(ctx, &out); err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

// AdminSearchCircuitTimesByName joint users sur prénom/nom (contient, insensible à la casse).
func (d *DB) AdminSearchCircuitTimesByName(ctx context.Context, firstName, lastName string, skip, limit int64) ([]models.CircuitTime, []models.User, int64, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	fn := strings.TrimSpace(firstName)
	ln := strings.TrimSpace(lastName)
	userFilter := bson.M{}
	if fn != "" {
		userFilter["first_name"] = bson.M{"$regex": escapeRegex(fn), "$options": "i"}
	}
	if ln != "" {
		userFilter["last_name"] = bson.M{"$regex": escapeRegex(ln), "$options": "i"}
	}
	if len(userFilter) == 0 {
		return nil, nil, 0, nil
	}
	cur, err := d.users.Find(ctx, userFilter, options.Find().SetProjection(bson.M{"_id": 1, "first_name": 1, "last_name": 1, "email": 1}).SetLimit(500))
	if err != nil {
		return nil, nil, 0, err
	}
	defer cur.Close(ctx)
	var users []models.User
	if err := cur.All(ctx, &users); err != nil {
		return nil, nil, 0, err
	}
	if len(users) == 0 {
		return nil, nil, 0, nil
	}
	ids := make([]primitive.ObjectID, 0, len(users))
	uMap := make(map[primitive.ObjectID]models.User, len(users))
	for _, u := range users {
		ids = append(ids, u.ID)
		uMap[u.ID] = u
	}
	tfilter := bson.M{"user_id": bson.M{"$in": ids}}
	total, err := d.circuitTimes.CountDocuments(ctx, tfilter)
	if err != nil {
		return nil, nil, 0, err
	}
	opts := options.Find().SetSkip(skip).SetLimit(limit).SetSort(bson.D{{Key: "created_at", Value: -1}})
	tcur, err := d.circuitTimes.Find(ctx, tfilter, opts)
	if err != nil {
		return nil, nil, 0, err
	}
	defer tcur.Close(ctx)
	var times []models.CircuitTime
	if err := tcur.All(ctx, &times); err != nil {
		return nil, nil, 0, err
	}
	return times, users, total, nil
}
