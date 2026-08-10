package store

import (
	"context"
	"time"

	"runapp/internal/models"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// InsertAdminNotification enregistre l’évènement et renvoie la version persistée (avec _id).
func (d *DB) InsertAdminNotification(ctx context.Context, n *models.AdminNotification) error {
	if n.CreatedAt.IsZero() {
		n.CreatedAt = time.Now().UTC()
	}
	res, err := d.adminNotifications.InsertOne(ctx, n)
	if err != nil {
		return err
	}
	if oid, ok := res.InsertedID.(primitive.ObjectID); ok {
		n.ID = oid
	}
	return nil
}

// ListAdminNotifications renvoie les évènements du plus récent au plus ancien.
func (d *DB) ListAdminNotifications(ctx context.Context, limit int64) ([]models.AdminNotification, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	opts := options.Find().SetSort(bson.D{{Key: "created_at", Value: -1}}).SetLimit(limit)
	cur, err := d.adminNotifications.Find(ctx, bson.M{}, opts)
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	out := make([]models.AdminNotification, 0, limit)
	if err := cur.All(ctx, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (d *DB) CountUnreadAdminNotifications(ctx context.Context) (int64, error) {
	return d.adminNotifications.CountDocuments(ctx, bson.M{"read_at": bson.M{"$exists": false}})
}

// MarkAdminNotificationsRead marque tout ce qui date d’avant `until` comme lu.
func (d *DB) MarkAdminNotificationsRead(ctx context.Context, until time.Time) error {
	_, err := d.adminNotifications.UpdateMany(
		ctx,
		bson.M{"read_at": bson.M{"$exists": false}, "created_at": bson.M{"$lte": until}},
		bson.M{"$set": bson.M{"read_at": time.Now().UTC()}},
	)
	return err
}

// UpsertAdminPushToken lie un jeton Expo au compte admin courant. Un même appareil qui change
// de compte écrase l’entrée précédente (index unique sur token).
func (d *DB) UpsertAdminPushToken(ctx context.Context, token string, userID primitive.ObjectID, platform string) error {
	now := time.Now().UTC()
	_, err := d.adminPushTokens.UpdateOne(
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

func (d *DB) DeleteAdminPushToken(ctx context.Context, token string) error {
	_, err := d.adminPushTokens.DeleteOne(ctx, bson.M{"token": token})
	return err
}

func (d *DB) DeleteAdminPushTokens(ctx context.Context, tokens []string) error {
	if len(tokens) == 0 {
		return nil
	}
	_, err := d.adminPushTokens.DeleteMany(ctx, bson.M{"token": bson.M{"$in": tokens}})
	return err
}

// DeleteAdminPushTokensByUser purge les jetons d’un compte (déconnexion, perte du rôle, suppression).
func (d *DB) DeleteAdminPushTokensByUser(ctx context.Context, userID primitive.ObjectID) error {
	_, err := d.adminPushTokens.DeleteMany(ctx, bson.M{"user_id": userID})
	return err
}

// ListActiveAdminPushTokens ne renvoie que les jetons dont le compte lié est encore admin :
// c’est ce filtre qui garantit qu’un compte rétrogradé ne reçoit plus rien.
func (d *DB) ListActiveAdminPushTokens(ctx context.Context) ([]string, error) {
	cur, err := d.adminPushTokens.Find(ctx, bson.M{})
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var rows []models.AdminPushToken
	if err := cur.All(ctx, &rows); err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}

	ids := make([]primitive.ObjectID, 0, len(rows))
	seen := make(map[primitive.ObjectID]bool, len(rows))
	for _, r := range rows {
		if !seen[r.UserID] {
			seen[r.UserID] = true
			ids = append(ids, r.UserID)
		}
	}
	adminCur, err := d.users.Find(
		ctx,
		bson.M{"_id": bson.M{"$in": ids}, "role": models.RoleAdmin},
		options.Find().SetProjection(bson.M{"_id": 1}),
	)
	if err != nil {
		return nil, err
	}
	defer adminCur.Close(ctx)
	admins := make(map[primitive.ObjectID]bool, len(ids))
	for adminCur.Next(ctx) {
		var raw struct {
			ID primitive.ObjectID `bson:"_id"`
		}
		if err := adminCur.Decode(&raw); err != nil {
			return nil, err
		}
		admins[raw.ID] = true
	}

	out := make([]string, 0, len(rows))
	stale := make([]string, 0)
	for _, r := range rows {
		if admins[r.UserID] {
			out = append(out, r.Token)
		} else {
			stale = append(stale, r.Token)
		}
	}
	// Le compte n’est plus admin (ou n’existe plus) : le jeton ne sert plus à rien.
	_ = d.DeleteAdminPushTokens(ctx, stale)
	return out, nil
}
