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

type DB struct {
	client   *mongo.Client
	database *mongo.Database
	users    *mongo.Collection
}

func Connect(uri, dbName string) (*DB, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client, err := mongo.Connect(ctx, options.Client().ApplyURI(uri))
	if err != nil {
		return nil, err
	}
	if err := client.Ping(ctx, nil); err != nil {
		return nil, err
	}

	database := client.Database(dbName)
	users := database.Collection("users")
	_, _ = users.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "email", Value: 1}},
		Options: options.Index().SetUnique(true),
	})
	return &DB{
		client:   client,
		database: database,
		users:    users,
	}, nil
}

func (d *DB) Close(ctx context.Context) error {
	return d.client.Disconnect(ctx)
}

func (d *DB) CreateUser(ctx context.Context, email, passwordHash string) (*models.User, error) {
	now := time.Now().UTC()
	u := models.User{
		ID:           primitive.NewObjectID(),
		Email:        email,
		PasswordHash: passwordHash,
		CreatedAt:    now,
	}
	_, err := d.users.InsertOne(ctx, u)
	if err != nil {
		if mongo.IsDuplicateKeyError(err) {
			return nil, ErrDuplicateEmail
		}
		return nil, err
	}
	return &u, nil
}

func (d *DB) FindUserByEmail(ctx context.Context, email string) (*models.User, error) {
	var u models.User
	err := d.users.FindOne(ctx, bson.M{"email": email}).Decode(&u)
	if err == mongo.ErrNoDocuments {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
}

func (d *DB) FindUserByID(ctx context.Context, id primitive.ObjectID) (*models.User, error) {
	var u models.User
	err := d.users.FindOne(ctx, bson.M{"_id": id}).Decode(&u)
	if err == mongo.ErrNoDocuments {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
}

func (d *DB) UpdateStravaTokens(ctx context.Context, userID primitive.ObjectID, t models.StravaTokens) error {
	_, err := d.users.UpdateOne(ctx,
		bson.M{"_id": userID},
		bson.M{"$set": bson.M{"strava": t}},
	)
	return err
}
