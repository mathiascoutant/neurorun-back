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

const shopSettingsKey = "shop_settings"

// ErrShopSlugTaken : un autre article porte déjà cette adresse.
var ErrShopSlugTaken = errors.New("adresse d’article déjà utilisée")

func (d *DB) GetShopSettings(ctx context.Context) (models.ShopSettings, error) {
	var doc struct {
		Value models.ShopSettings `bson:"value"`
	}
	err := d.settings.FindOne(ctx, bson.M{"_id": shopSettingsKey}).Decode(&doc)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return models.DefaultShopSettings(), nil
	}
	if err != nil {
		return models.ShopSettings{}, err
	}
	return doc.Value, nil
}

func (d *DB) UpsertShopSettings(ctx context.Context, s models.ShopSettings) error {
	_, err := d.settings.UpdateOne(ctx,
		bson.M{"_id": shopSettingsKey},
		bson.M{"$set": bson.M{"value": s}},
		options.Update().SetUpsert(true),
	)
	return err
}

// ListShopProducts : dans l’ordre du catalogue ; `onlyActive` masque les brouillons.
func (d *DB) ListShopProducts(ctx context.Context, onlyActive bool) ([]models.ShopProduct, error) {
	filter := bson.M{}
	if onlyActive {
		filter["active"] = true
	}
	cur, err := d.shopProducts.Find(ctx, filter, options.Find().SetSort(bson.D{
		{Key: "position", Value: 1}, {Key: "created_at", Value: 1},
	}))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	out := []models.ShopProduct{}
	if err := cur.All(ctx, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (d *DB) GetShopProduct(ctx context.Context, id primitive.ObjectID) (*models.ShopProduct, error) {
	var p models.ShopProduct
	err := d.shopProducts.FindOne(ctx, bson.M{"_id": id}).Decode(&p)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	return &p, err
}

func (d *DB) GetShopProductBySlug(ctx context.Context, slug string) (*models.ShopProduct, error) {
	var p models.ShopProduct
	err := d.shopProducts.FindOne(ctx, bson.M{"slug": slug}).Decode(&p)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	return &p, err
}

// NextShopProductPosition : un nouvel article se range à la fin du catalogue.
func (d *DB) NextShopProductPosition(ctx context.Context) (int, error) {
	var last models.ShopProduct
	err := d.shopProducts.FindOne(ctx, bson.M{},
		options.FindOne().SetSort(bson.M{"position": -1})).Decode(&last)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return last.Position + 1, nil
}

func (d *DB) CreateShopProduct(ctx context.Context, p *models.ShopProduct) error {
	res, err := d.shopProducts.InsertOne(ctx, p)
	if mongo.IsDuplicateKeyError(err) {
		return ErrShopSlugTaken
	}
	if err != nil {
		return err
	}
	if oid, ok := res.InsertedID.(primitive.ObjectID); ok {
		p.ID = oid
	}
	return nil
}

// UpdateShopProduct enregistre la fiche. Le stock passe par `$set` : une vente réglée à la seconde
// près peut être écrasée, un risque accepté pour un écran utilisé par un seul administrateur.
func (d *DB) UpdateShopProduct(ctx context.Context, p *models.ShopProduct) error {
	set := bson.M{
		"slug":              p.Slug,
		"name":              p.Name,
		"description":       p.Description,
		"active":            p.Active,
		"price_cents":       p.PriceCents,
		"promo_price_cents": p.PromoPriceCents,
		"sizes":             p.Sizes,
		"media":             p.Media,
		"updated_at":        time.Now().UTC(),
	}
	update := bson.M{"$set": set}
	if p.PromoEndsAt != nil {
		set["promo_ends_at"] = p.PromoEndsAt
	} else {
		update["$unset"] = bson.M{"promo_ends_at": ""}
	}
	res, err := d.shopProducts.UpdateOne(ctx, bson.M{"_id": p.ID}, update)
	if mongo.IsDuplicateKeyError(err) {
		return ErrShopSlugTaken
	}
	if err != nil {
		return err
	}
	if res.MatchedCount == 0 {
		return ErrNotFound
	}
	return nil
}

// AddShopMedia ajoute un fichier à la fin de la galerie.
func (d *DB) AddShopMedia(ctx context.Context, id primitive.ObjectID, m models.ShopMedia) error {
	res, err := d.shopProducts.UpdateOne(ctx, bson.M{"_id": id}, bson.M{
		"$push": bson.M{"media": m},
		"$set":  bson.M{"updated_at": time.Now().UTC()},
	})
	if err != nil {
		return err
	}
	if res.MatchedCount == 0 {
		return ErrNotFound
	}
	return nil
}

func (d *DB) RemoveShopMedia(ctx context.Context, id primitive.ObjectID, mediaID string) error {
	res, err := d.shopProducts.UpdateOne(ctx, bson.M{"_id": id}, bson.M{
		"$pull": bson.M{"media": bson.M{"id": mediaID}},
		"$set":  bson.M{"updated_at": time.Now().UTC()},
	})
	if err != nil {
		return err
	}
	if res.MatchedCount == 0 {
		return ErrNotFound
	}
	return nil
}

// ReorderShopProducts : l’ordre du tableau devient l’ordre du catalogue.
func (d *DB) ReorderShopProducts(ctx context.Context, ids []primitive.ObjectID) error {
	for i, id := range ids {
		if _, err := d.shopProducts.UpdateOne(ctx, bson.M{"_id": id},
			bson.M{"$set": bson.M{"position": i}}); err != nil {
			return err
		}
	}
	return nil
}

func (d *DB) DeleteShopProduct(ctx context.Context, id primitive.ObjectID) error {
	res, err := d.shopProducts.DeleteOne(ctx, bson.M{"_id": id})
	if err != nil {
		return err
	}
	if res.DeletedCount == 0 {
		return ErrNotFound
	}
	return nil
}

// ReserveShopStock retire `qty` du stock d’une taille, en une seule opération : deux clients ne
// peuvent pas réserver la dernière pièce. false = plus assez de stock.
func (d *DB) ReserveShopStock(ctx context.Context, id primitive.ObjectID, size string, qty int) (bool, error) {
	res, err := d.shopProducts.UpdateOne(ctx, bson.M{
		"_id":    id,
		"active": true,
		"sizes":  bson.M{"$elemMatch": bson.M{"label": size, "stock": bson.M{"$gte": qty}}},
	}, bson.M{"$inc": bson.M{"sizes.$.stock": -qty}})
	if err != nil {
		return false, err
	}
	return res.ModifiedCount == 1, nil
}

// ReleaseShopStock rend une réservation. La taille a pu être supprimée entre-temps : rien à rendre.
func (d *DB) ReleaseShopStock(ctx context.Context, id primitive.ObjectID, size string, qty int) error {
	_, err := d.shopProducts.UpdateOne(ctx,
		bson.M{"_id": id, "sizes.label": size},
		bson.M{"$inc": bson.M{"sizes.$.stock": qty}},
	)
	return err
}

func (d *DB) CreateShopOrder(ctx context.Context, o *models.ShopOrder) error {
	res, err := d.shopOrders.InsertOne(ctx, o)
	if err != nil {
		return err
	}
	if oid, ok := res.InsertedID.(primitive.ObjectID); ok {
		o.ID = oid
	}
	return nil
}

func (d *DB) SetShopOrderSession(ctx context.Context, id primitive.ObjectID, sessionID string) error {
	_, err := d.shopOrders.UpdateOne(ctx, bson.M{"_id": id},
		bson.M{"$set": bson.M{"stripe_session_id": sessionID}})
	return err
}

func (d *DB) GetShopOrder(ctx context.Context, id primitive.ObjectID) (*models.ShopOrder, error) {
	var o models.ShopOrder
	err := d.shopOrders.FindOne(ctx, bson.M{"_id": id}).Decode(&o)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	return &o, err
}

// TransitionShopOrder change l’état seulement depuis `from`. Le retour du client et le webhook
// traitent souvent le même paiement : seul le gagnant reçoit true, et n’agit qu’une fois.
func (d *DB) TransitionShopOrder(ctx context.Context, id primitive.ObjectID, from, to string, extra bson.M) (bool, error) {
	set := bson.M{"status": to}
	for k, v := range extra {
		set[k] = v
	}
	res, err := d.shopOrders.UpdateOne(ctx, bson.M{"_id": id, "status": from}, bson.M{"$set": set})
	if err != nil {
		return false, err
	}
	return res.ModifiedCount == 1, nil
}

// ListShopOrders : les plus récentes d’abord. Les commandes en attente ou expirées n’ont pas été
// payées : `status` vide renvoie seulement celles à traiter ou déjà traitées.
func (d *DB) ListShopOrders(ctx context.Context, status string, limit int64) ([]models.ShopOrder, error) {
	filter := bson.M{"status": bson.M{"$in": []string{
		models.ShopOrderPaid, models.ShopOrderShipped, models.ShopOrderCanceled,
	}}}
	if status != "" {
		filter = bson.M{"status": status}
	}
	cur, err := d.shopOrders.Find(ctx, filter,
		options.Find().SetSort(bson.M{"created_at": -1}).SetLimit(limit))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	out := []models.ShopOrder{}
	if err := cur.All(ctx, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ListStaleShopOrders : commandes en attente dont la réservation a dépassé sa durée.
func (d *DB) ListStaleShopOrders(ctx context.Context, before time.Time, limit int64) ([]models.ShopOrder, error) {
	cur, err := d.shopOrders.Find(ctx, bson.M{
		"status":     models.ShopOrderPending,
		"expires_at": bson.M{"$lt": before},
	}, options.Find().SetLimit(limit))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	out := []models.ShopOrder{}
	if err := cur.All(ctx, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ForceShopOrderExpiryForTest : sert aux tests, pour simuler une réservation dépassée.
func (d *DB) ForceShopOrderExpiryForTest(ctx context.Context, id primitive.ObjectID, at time.Time) error {
	_, err := d.shopOrders.UpdateOne(ctx, bson.M{"_id": id}, bson.M{"$set": bson.M{"expires_at": at}})
	return err
}
