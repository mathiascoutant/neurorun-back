package store

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"

	"runapp/internal/models"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// ErrFriendshipExists : une demande est déjà en attente, ou les deux comptes sont déjà amis.
var ErrFriendshipExists = errors.New("friendship already exists")

// pairFilter matche la relation entre deux comptes quel que soit qui a demandé.
func pairFilter(a, b primitive.ObjectID) bson.M {
	return bson.M{"$or": []bson.M{
		{"requester_id": a, "addressee_id": b},
		{"requester_id": b, "addressee_id": a},
	}}
}

// FriendshipBetween renvoie la relation entre deux comptes, ou ErrNotFound.
func (d *DB) FriendshipBetween(ctx context.Context, a, b primitive.ObjectID) (*models.Friendship, error) {
	var f models.Friendship
	err := d.friendships.FindOne(ctx, pairFilter(a, b)).Decode(&f)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &f, nil
}

// SendFriendRequest crée une demande en attente. Refuse si une relation existe déjà
// dans un sens ou dans l'autre — y compris une demande que l'autre a déjà envoyée.
func (d *DB) SendFriendRequest(ctx context.Context, from, to primitive.ObjectID) (*models.Friendship, error) {
	if from == to {
		return nil, ErrFriendshipExists
	}
	if _, err := d.FriendshipBetween(ctx, from, to); err == nil {
		return nil, ErrFriendshipExists
	} else if !errors.Is(err, ErrNotFound) {
		return nil, err
	}

	f := models.Friendship{
		ID:          primitive.NewObjectID(),
		RequesterID: from,
		AddresseeID: to,
		Status:      models.FriendshipPending,
		CreatedAt:   time.Now().UTC(),
	}
	if _, err := d.friendships.InsertOne(ctx, f); err != nil {
		if mongo.IsDuplicateKeyError(err) {
			return nil, ErrFriendshipExists
		}
		return nil, err
	}
	return &f, nil
}

// AcceptFriendRequest : seul le destinataire peut accepter, et seulement tant que
// la demande est en attente.
func (d *DB) AcceptFriendRequest(ctx context.Context, requestID, addresseeID primitive.ObjectID) (*models.Friendship, error) {
	now := time.Now().UTC()
	var f models.Friendship
	err := d.friendships.FindOneAndUpdate(
		ctx,
		bson.M{"_id": requestID, "addressee_id": addresseeID, "status": models.FriendshipPending},
		bson.M{"$set": bson.M{"status": models.FriendshipAccepted, "responded_at": now}},
		options.FindOneAndUpdate().SetReturnDocument(options.After),
	).Decode(&f)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &f, nil
}

// DeclineFriendRequest supprime la demande : un refus ne doit pas empêcher une
// nouvelle tentative plus tard.
func (d *DB) DeclineFriendRequest(ctx context.Context, requestID, addresseeID primitive.ObjectID) error {
	res, err := d.friendships.DeleteOne(ctx, bson.M{
		"_id":          requestID,
		"addressee_id": addresseeID,
		"status":       models.FriendshipPending,
	})
	if err != nil {
		return err
	}
	if res.DeletedCount == 0 {
		return ErrNotFound
	}
	return nil
}

// CancelFriendRequest : l'émetteur retire sa propre demande en attente.
func (d *DB) CancelFriendRequest(ctx context.Context, requestID, requesterID primitive.ObjectID) error {
	res, err := d.friendships.DeleteOne(ctx, bson.M{
		"_id":          requestID,
		"requester_id": requesterID,
		"status":       models.FriendshipPending,
	})
	if err != nil {
		return err
	}
	if res.DeletedCount == 0 {
		return ErrNotFound
	}
	return nil
}

// RemoveFriend rompt une amitié acceptée, dans un sens ou dans l'autre.
func (d *DB) RemoveFriend(ctx context.Context, userID, otherID primitive.ObjectID) error {
	filter := pairFilter(userID, otherID)
	filter["status"] = models.FriendshipAccepted
	res, err := d.friendships.DeleteOne(ctx, filter)
	if err != nil {
		return err
	}
	if res.DeletedCount == 0 {
		return ErrNotFound
	}
	return nil
}

// ListFriendIDs renvoie les comptes liés par une amitié acceptée.
func (d *DB) ListFriendIDs(ctx context.Context, userID primitive.ObjectID) ([]primitive.ObjectID, error) {
	filter := bson.M{
		"status": models.FriendshipAccepted,
		"$or": []bson.M{
			{"requester_id": userID},
			{"addressee_id": userID},
		},
	}
	cur, err := d.friendships.Find(ctx, filter)
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var rows []models.Friendship
	if err := cur.All(ctx, &rows); err != nil {
		return nil, err
	}
	out := make([]primitive.ObjectID, 0, len(rows))
	for i := range rows {
		out = append(out, rows[i].Other(userID))
	}
	return out, nil
}

// AreFriends : true si une amitié acceptée lie les deux comptes.
func (d *DB) AreFriends(ctx context.Context, a, b primitive.ObjectID) (bool, error) {
	filter := pairFilter(a, b)
	filter["status"] = models.FriendshipAccepted
	n, err := d.friendships.CountDocuments(ctx, filter, options.Count().SetLimit(1))
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// ListPendingRequests renvoie les demandes en attente reçues (incoming) et envoyées (outgoing).
func (d *DB) ListPendingRequests(ctx context.Context, userID primitive.ObjectID) (incoming, outgoing []models.Friendship, err error) {
	cur, err := d.friendships.Find(
		ctx,
		bson.M{
			"status": models.FriendshipPending,
			"$or": []bson.M{
				{"requester_id": userID},
				{"addressee_id": userID},
			},
		},
		options.Find().SetSort(bson.D{{Key: "created_at", Value: -1}}),
	)
	if err != nil {
		return nil, nil, err
	}
	defer cur.Close(ctx)
	var rows []models.Friendship
	if err := cur.All(ctx, &rows); err != nil {
		return nil, nil, err
	}
	for i := range rows {
		if rows[i].AddresseeID == userID {
			incoming = append(incoming, rows[i])
		} else {
			outgoing = append(outgoing, rows[i])
		}
	}
	return incoming, outgoing, nil
}

// CountIncomingFriendRequests alimente la pastille de l'onglet Boost.
func (d *DB) CountIncomingFriendRequests(ctx context.Context, userID primitive.ObjectID) (int64, error) {
	return d.friendships.CountDocuments(ctx, bson.M{
		"addressee_id": userID,
		"status":       models.FriendshipPending,
	})
}

// SearchUsersByName cherche sur prénom, nom, et « prénom nom » concaténé — sans quoi
// taper le nom complet ne renverrait jamais rien.
func (d *DB) SearchUsersByName(ctx context.Context, q string, exclude primitive.ObjectID, limit int64) ([]models.User, error) {
	q = strings.TrimSpace(q)
	if len(q) < 2 {
		return nil, nil
	}
	if limit <= 0 || limit > 50 {
		limit = 20
	}

	terms := strings.Fields(q)
	and := make([]bson.M, 0, len(terms))
	for _, t := range terms {
		p := regexp.QuoteMeta(t)
		and = append(and, bson.M{"$or": []bson.M{
			{"first_name": bson.M{"$regex": p, "$options": "i"}},
			{"last_name": bson.M{"$regex": p, "$options": "i"}},
		}})
	}
	filter := bson.M{"_id": bson.M{"$ne": exclude}, "$and": and}

	cur, err := d.users.Find(ctx, filter, options.Find().
		SetLimit(limit).
		SetProjection(bson.M{"_id": 1, "first_name": 1, "last_name": 1, "created_at": 1}))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var out []models.User
	if err := cur.All(ctx, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// FindUsersByIDs charge plusieurs comptes en une requête. Seuls les champs affichés
// dans le fil et les listes d'amis sont projetés.
func (d *DB) FindUsersByIDs(ctx context.Context, ids []primitive.ObjectID) (map[primitive.ObjectID]models.User, error) {
	out := make(map[primitive.ObjectID]models.User, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	cur, err := d.users.Find(ctx,
		bson.M{"_id": bson.M{"$in": ids}},
		options.Find().SetProjection(bson.M{"_id": 1, "first_name": 1, "last_name": 1}),
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

// FriendshipsWith renvoie, pour chaque compte de others, la relation avec userID si
// elle existe — en une requête, pour ne pas interroger la base par résultat de recherche.
func (d *DB) FriendshipsWith(ctx context.Context, userID primitive.ObjectID, others []primitive.ObjectID) (map[primitive.ObjectID]models.Friendship, error) {
	out := make(map[primitive.ObjectID]models.Friendship, len(others))
	if len(others) == 0 {
		return out, nil
	}
	cur, err := d.friendships.Find(ctx, bson.M{"$or": []bson.M{
		{"requester_id": userID, "addressee_id": bson.M{"$in": others}},
		{"addressee_id": userID, "requester_id": bson.M{"$in": others}},
	}})
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var rows []models.Friendship
	if err := cur.All(ctx, &rows); err != nil {
		return nil, err
	}
	for i := range rows {
		out[rows[i].Other(userID)] = rows[i]
	}
	return out, nil
}

// ListLiveRunsByUsersBefore : fil des courses de plusieurs comptes, du plus récent au plus
// ancien. La trace GPS est écartée de la projection — un fil de 20 courses la chargerait
// pour rien et pèserait plusieurs mégaoctets.
func (d *DB) ListLiveRunsByUsersBefore(ctx context.Context, userIDs []primitive.ObjectID, before time.Time, limit int) ([]models.LiveRun, error) {
	if len(userIDs) == 0 {
		return nil, nil
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	filter := bson.M{"user_id": bson.M{"$in": userIDs}}
	if !before.IsZero() {
		filter["created_at"] = bson.M{"$lt": before.UTC()}
	}
	opts := options.Find().
		SetSort(bson.D{{Key: "created_at", Value: -1}}).
		SetLimit(int64(limit)).
		SetProjection(bson.M{"track_points": 0})
	cur, err := d.liveRuns.Find(ctx, filter, opts)
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

// GetLiveRunByID récupère une course sans filtrer sur son propriétaire : l'appelant
// doit vérifier lui-même le droit d'accès (propriétaire ou ami).
func (d *DB) GetLiveRunByID(ctx context.Context, runID primitive.ObjectID) (*models.LiveRun, error) {
	var lr models.LiveRun
	err := d.liveRuns.FindOne(ctx, bson.M{"_id": runID}).Decode(&lr)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &lr, nil
}

// GetLiveRunMetaByID : propriétaire et chiffres de la course, sans la trace GPS.
// Poser un boost n'a pas besoin des points, qui pèsent plusieurs mégaoctets.
func (d *DB) GetLiveRunMetaByID(ctx context.Context, runID primitive.ObjectID) (*models.LiveRun, error) {
	var lr models.LiveRun
	err := d.liveRuns.FindOne(ctx,
		bson.M{"_id": runID},
		options.FindOne().SetProjection(bson.M{"track_points": 0, "splits": 0}),
	).Decode(&lr)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &lr, nil
}

// UpsertBoost pose ou change la réaction d'un utilisateur sur une course.
// changed vaut false quand la même réaction était déjà là : l'appelant s'en sert
// pour ne pas renvoyer une notification identique.
func (d *DB) UpsertBoost(ctx context.Context, runID, runOwnerID, fromUserID primitive.ObjectID, kind string) (changed bool, err error) {
	now := time.Now().UTC()
	var existing models.Boost
	err = d.boosts.FindOne(ctx, bson.M{"run_id": runID, "from_user_id": fromUserID}).Decode(&existing)
	switch {
	case err == nil:
		if existing.Kind == kind {
			return false, nil
		}
		_, err = d.boosts.UpdateOne(ctx,
			bson.M{"_id": existing.ID},
			bson.M{"$set": bson.M{"kind": kind, "updated_at": now}},
		)
		return err == nil, err
	case errors.Is(err, mongo.ErrNoDocuments):
		_, err = d.boosts.InsertOne(ctx, models.Boost{
			ID:         primitive.NewObjectID(),
			RunID:      runID,
			RunOwnerID: runOwnerID,
			FromUserID: fromUserID,
			Kind:       kind,
			CreatedAt:  now,
			UpdatedAt:  now,
		})
		if mongo.IsDuplicateKeyError(err) {
			// Course concurrente sur le même couple : la réaction est posée, rien à notifier.
			return false, nil
		}
		return err == nil, err
	default:
		return false, err
	}
}

// RemoveBoost retire la réaction d'un utilisateur sur une course.
func (d *DB) RemoveBoost(ctx context.Context, runID, fromUserID primitive.ObjectID) error {
	_, err := d.boosts.DeleteOne(ctx, bson.M{"run_id": runID, "from_user_id": fromUserID})
	return err
}

// ListBoostsForRuns renvoie toutes les réactions des courses données, groupées par course.
func (d *DB) ListBoostsForRuns(ctx context.Context, runIDs []primitive.ObjectID) (map[primitive.ObjectID][]models.Boost, error) {
	if len(runIDs) == 0 {
		return map[primitive.ObjectID][]models.Boost{}, nil
	}
	cur, err := d.boosts.Find(ctx, bson.M{"run_id": bson.M{"$in": runIDs}})
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var rows []models.Boost
	if err := cur.All(ctx, &rows); err != nil {
		return nil, err
	}
	out := make(map[primitive.ObjectID][]models.Boost, len(runIDs))
	for i := range rows {
		out[rows[i].RunID] = append(out[rows[i].RunID], rows[i])
	}
	return out, nil
}

// UpsertPushToken lie un appareil au compte courant. Un même appareil qui change de
// compte écrase la ligne précédente (index unique sur token).
func (d *DB) UpsertPushToken(ctx context.Context, token string, userID primitive.ObjectID, platform string) error {
	now := time.Now().UTC()
	_, err := d.pushTokens.UpdateOne(
		ctx,
		bson.M{"token": token},
		bson.M{
			"$set":         bson.M{"user_id": userID, "platform": platform, "updated_at": now},
			"$setOnInsert": bson.M{"token": token, "created_at": now},
		},
		options.Update().SetUpsert(true),
	)
	return err
}

func (d *DB) DeletePushToken(ctx context.Context, token string) error {
	_, err := d.pushTokens.DeleteOne(ctx, bson.M{"token": token})
	return err
}

func (d *DB) DeletePushTokens(ctx context.Context, tokens []string) error {
	if len(tokens) == 0 {
		return nil
	}
	_, err := d.pushTokens.DeleteMany(ctx, bson.M{"token": bson.M{"$in": tokens}})
	return err
}

func (d *DB) DeletePushTokensByUser(ctx context.Context, userID primitive.ObjectID) error {
	_, err := d.pushTokens.DeleteMany(ctx, bson.M{"user_id": userID})
	return err
}

// ListPushTokensByUser : appareils à notifier pour un compte.
func (d *DB) ListPushTokensByUser(ctx context.Context, userID primitive.ObjectID) ([]string, error) {
	cur, err := d.pushTokens.Find(ctx, bson.M{"user_id": userID})
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var rows []models.PushToken
	if err := cur.All(ctx, &rows); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows))
	for i := range rows {
		out = append(out, rows[i].Token)
	}
	return out, nil
}
