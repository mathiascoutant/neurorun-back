package store

import (
	"context"
	"errors"
	"time"

	"runapp/internal/models"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
)

// ErrRefreshReplay : le jeton présenté a déjà été échangé. Soit le client a
// rejoué une requête, soit le jeton a été volé — on ne sait pas lequel, donc on
// coupe toute la lignée et on renvoie l’utilisateur vers la connexion.
var ErrRefreshReplay = errors.New("refresh token already used")

// CreateRefreshToken ouvre une nouvelle lignée : c’est le chemin d’une
// connexion ou d’une inscription. Passer un familyID non nul prolonge une
// lignée existante (rotation).
func (d *DB) CreateRefreshToken(
	ctx context.Context,
	userID primitive.ObjectID,
	tokenHash string,
	familyID primitive.ObjectID,
	ttl time.Duration,
	device string,
) (*models.RefreshToken, error) {
	now := time.Now().UTC()
	if familyID.IsZero() {
		familyID = primitive.NewObjectID()
	}
	t := models.RefreshToken{
		ID:        primitive.NewObjectID(),
		TokenHash: tokenHash,
		UserID:    userID,
		FamilyID:  familyID,
		CreatedAt: now,
		ExpiresAt: now.Add(ttl),
		Device:    device,
	}
	if _, err := d.refreshTokens.InsertOne(ctx, t); err != nil {
		return nil, err
	}
	return &t, nil
}

// ConsumeRefreshToken marque le jeton comme échangé et le renvoie, en une seule
// écriture conditionnelle : deux requêtes de refresh simultanées ne peuvent pas
// réussir toutes les deux, seule la première trouve le jeton encore vierge.
//
// Un jeton connu mais déjà utilisé remonte ErrRefreshReplay (et sa lignée est
// coupée) ; un jeton inconnu, révoqué ou expiré remonte ErrNotFound.
func (d *DB) ConsumeRefreshToken(ctx context.Context, tokenHash string) (*models.RefreshToken, error) {
	now := time.Now().UTC()
	filter := bson.M{
		"token_hash": tokenHash,
		"used_at":    bson.M{"$exists": false},
		"revoked_at": bson.M{"$exists": false},
		"expires_at": bson.M{"$gt": now},
	}
	update := bson.M{"$set": bson.M{"used_at": now}}

	var t models.RefreshToken
	err := d.refreshTokens.FindOneAndUpdate(ctx, filter, update).Decode(&t)
	if err == nil {
		return &t, nil
	}
	if !errors.Is(err, mongo.ErrNoDocuments) {
		return nil, err
	}

	/* Rien à échanger. Reste à savoir si c’est un jeton qu’on a déjà honoré :
	   dans ce cas quelqu’un rejoue une session, et la lignée entière saute. */
	var used models.RefreshToken
	if lookupErr := d.refreshTokens.FindOne(ctx, bson.M{"token_hash": tokenHash}).Decode(&used); lookupErr != nil {
		return nil, ErrNotFound
	}
	if used.UsedAt != nil && used.RevokedAt == nil {
		_ = d.RevokeRefreshFamily(ctx, used.FamilyID)
		return nil, ErrRefreshReplay
	}
	return nil, ErrNotFound
}

// RotateRefreshToken échange un jeton contre son successeur dans la même
// lignée. Renvoie le nouvel enregistrement.
func (d *DB) RotateRefreshToken(
	ctx context.Context,
	old *models.RefreshToken,
	newHash string,
	ttl time.Duration,
	device string,
) (*models.RefreshToken, error) {
	if device == "" {
		device = old.Device
	}
	return d.CreateRefreshToken(ctx, old.UserID, newHash, old.FamilyID, ttl, device)
}

// RevokeRefreshToken : déconnexion d’un seul appareil. Idempotent — un jeton
// déjà inconnu ou déjà coupé n’est pas une erreur.
func (d *DB) RevokeRefreshToken(ctx context.Context, tokenHash string) error {
	now := time.Now().UTC()
	_, err := d.refreshTokens.UpdateOne(
		ctx,
		bson.M{"token_hash": tokenHash, "revoked_at": bson.M{"$exists": false}},
		bson.M{"$set": bson.M{"revoked_at": now}},
	)
	return err
}

// RevokeRefreshFamily coupe une lignée entière : la session d’origine et toutes
// ses rotations.
func (d *DB) RevokeRefreshFamily(ctx context.Context, familyID primitive.ObjectID) error {
	now := time.Now().UTC()
	_, err := d.refreshTokens.UpdateMany(
		ctx,
		bson.M{"family_id": familyID, "revoked_at": bson.M{"$exists": false}},
		bson.M{"$set": bson.M{"revoked_at": now}},
	)
	return err
}

// RevokeAllRefreshTokensForUser : déconnexion de tous les appareils. Appelé sur
// changement de mot de passe et suppression de compte.
func (d *DB) RevokeAllRefreshTokensForUser(ctx context.Context, userID primitive.ObjectID) error {
	now := time.Now().UTC()
	_, err := d.refreshTokens.UpdateMany(
		ctx,
		bson.M{"user_id": userID, "revoked_at": bson.M{"$exists": false}},
		bson.M{"$set": bson.M{"revoked_at": now}},
	)
	return err
}
