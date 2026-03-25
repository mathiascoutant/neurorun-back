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
	client          *mongo.Client
	database        *mongo.Database
	users           *mongo.Collection
	conversations   *mongo.Collection
	goals           *mongo.Collection
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
	conversations := database.Collection("conversations")
	goals := database.Collection("goals")
	_, _ = users.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "email", Value: 1}},
		Options: options.Index().SetUnique(true),
	})
	_, _ = conversations.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{{Key: "user_id", Value: 1}, {Key: "updated_at", Value: -1}},
	})
	_, _ = goals.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{{Key: "user_id", Value: 1}, {Key: "created_at", Value: -1}},
	})
	return &DB{
		client:        client,
		database:      database,
		users:         users,
		conversations: conversations,
		goals:         goals,
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

func (d *DB) CreateConversation(ctx context.Context, userID primitive.ObjectID) (*models.Conversation, error) {
	now := time.Now().UTC()
	c := models.Conversation{
		ID:        primitive.NewObjectID(),
		UserID:    userID,
		Title:     "Nouvelle conversation",
		Messages:  []models.ChatTurn{},
		CreatedAt: now,
		UpdatedAt: now,
	}
	_, err := d.conversations.InsertOne(ctx, c)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func (d *DB) ListConversationsByUser(ctx context.Context, userID primitive.ObjectID) ([]models.ConversationListItem, error) {
	opts := options.Find().SetSort(bson.D{{Key: "updated_at", Value: -1}})
	cur, err := d.conversations.Find(ctx, bson.M{"user_id": userID}, opts)
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var out []models.ConversationListItem
	for cur.Next(ctx) {
		var item models.ConversationListItem
		if err := cur.Decode(&item); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, cur.Err()
}

func (d *DB) GetConversationByUser(ctx context.Context, userID, convID primitive.ObjectID) (*models.Conversation, error) {
	var c models.Conversation
	err := d.conversations.FindOne(ctx, bson.M{"_id": convID, "user_id": userID}).Decode(&c)
	if err == mongo.ErrNoDocuments {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func (d *DB) AppendConversationTurns(ctx context.Context, userID, convID primitive.ObjectID, userText, assistantText string, newTitle *string) error {
	now := time.Now().UTC()
	turns := []models.ChatTurn{
		{Role: "user", Text: userText, CreatedAt: now},
		{Role: "assistant", Text: assistantText, CreatedAt: now.Add(time.Millisecond)},
	}
	setDoc := bson.M{"updated_at": now}
	if newTitle != nil && *newTitle != "" {
		setDoc["title"] = *newTitle
	}
	_, err := d.conversations.UpdateOne(ctx,
		bson.M{"_id": convID, "user_id": userID},
		bson.M{
			"$push": bson.M{"messages": bson.M{"$each": turns}},
			"$set":  setDoc,
		},
	)
	return err
}

func (d *DB) CreateGoal(ctx context.Context, g *models.Goal) error {
	if g.ID.IsZero() {
		g.ID = primitive.NewObjectID()
	}
	if g.CreatedAt.IsZero() {
		g.CreatedAt = time.Now().UTC()
	}
	_, err := d.goals.InsertOne(ctx, g)
	return err
}

func (d *DB) ListGoalsByUser(ctx context.Context, userID primitive.ObjectID) ([]models.Goal, error) {
	opts := options.Find().
		SetSort(bson.D{{Key: "created_at", Value: -1}}).
		SetProjection(bson.M{"coach_thread": 0})
	cur, err := d.goals.Find(ctx, bson.M{"user_id": userID}, opts)
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var out []models.Goal
	for cur.Next(ctx) {
		var g models.Goal
		if err := cur.Decode(&g); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, cur.Err()
}

func (d *DB) GetGoalByUser(ctx context.Context, userID, goalID primitive.ObjectID) (*models.Goal, error) {
	var g models.Goal
	err := d.goals.FindOne(ctx, bson.M{"_id": goalID, "user_id": userID}).Decode(&g)
	if err == mongo.ErrNoDocuments {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &g, nil
}

func (d *DB) DeleteGoalByUser(ctx context.Context, userID, goalID primitive.ObjectID) error {
	res, err := d.goals.DeleteOne(ctx, bson.M{"_id": goalID, "user_id": userID})
	if err != nil {
		return err
	}
	if res.DeletedCount == 0 {
		return ErrNotFound
	}
	return nil
}

func (d *DB) AppendGoalCoachTurns(ctx context.Context, userID, goalID primitive.ObjectID, userText, assistantText string) error {
	now := time.Now().UTC()
	turns := []models.ChatTurn{
		{Role: "user", Text: userText, CreatedAt: now},
		{Role: "assistant", Text: assistantText, CreatedAt: now.Add(time.Millisecond)},
	}
	_, err := d.goals.UpdateOne(ctx,
		bson.M{"_id": goalID, "user_id": userID},
		bson.M{"$push": bson.M{"coach_thread": bson.M{"$each": turns}}},
	)
	return err
}
