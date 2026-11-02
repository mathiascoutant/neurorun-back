package store

import (
	"context"
	"errors"
	"strconv"
	"time"

	"runapp/internal/models"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// shopModel3DMaxVersions : de quoi revenir en arrière, sans grossir vers la limite d'un document.
const shopModel3DMaxVersions = 15

// ErrShopModel3DFull : le nombre maximal de photos est atteint.
var ErrShopModel3DFull = errors.New("trop de photos")

// GetShopModel3D renvoie un travail vide si l'article n'a encore rien de modélisé.
func (d *DB) GetShopModel3D(ctx context.Context, productID primitive.ObjectID) (*models.ShopModel3D, error) {
	var m models.ShopModel3D
	err := d.shopModel3D.FindOne(ctx, bson.M{"_id": productID}).Decode(&m)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return &models.ShopModel3D{ProductID: productID, Status: models.ShopModel3DIdle}, nil
	}
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// AddShopModel3DPhoto : la limite est dans le filtre, pour tenir face à des envois simultanés. Un
// document plein ne correspond plus et l'insertion qui suit heurte son _id.
func (d *DB) AddShopModel3DPhoto(ctx context.Context, productID primitive.ObjectID, photoID string, limit int) error {
	_, err := d.shopModel3D.UpdateOne(ctx,
		bson.M{"_id": productID, "photos." + strconv.Itoa(limit-1): bson.M{"$exists": false}},
		bson.M{"$push": bson.M{"photos": photoID}, "$setOnInsert": bson.M{"status": models.ShopModel3DIdle}},
		options.Update().SetUpsert(true),
	)
	if mongo.IsDuplicateKeyError(err) {
		return ErrShopModel3DFull
	}
	return err
}

func (d *DB) RemoveShopModel3DPhoto(ctx context.Context, productID primitive.ObjectID, photoID string) error {
	res, err := d.shopModel3D.UpdateOne(ctx,
		bson.M{"_id": productID, "photos": photoID},
		bson.M{"$pull": bson.M{"photos": photoID}},
	)
	if err != nil {
		return err
	}
	if res.MatchedCount == 0 {
		return ErrNotFound
	}
	return nil
}

// BeginShopModel3D réserve la génération. false : une autre est en cours, et chacune coûte. Une
// génération commencée avant `staleBefore` a été interrompue par un redémarrage et ne bloque plus.
func (d *DB) BeginShopModel3D(ctx context.Context, productID primitive.ObjectID, now, staleBefore time.Time) (bool, error) {
	res, err := d.shopModel3D.UpdateOne(ctx,
		bson.M{"_id": productID, "$or": bson.A{
			bson.M{"status": bson.M{"$ne": models.ShopModel3DGenerating}},
			bson.M{"started_at": bson.M{"$lt": staleBefore}},
		}},
		bson.M{"$set": bson.M{"status": models.ShopModel3DGenerating, "started_at": now, "error": ""}},
	)
	if err != nil {
		return false, err
	}
	return res.MatchedCount > 0, nil
}

// FinishShopModel3D clôt la génération : nouvelle version, ou message d'erreur si `version` est nil.
func (d *DB) FinishShopModel3D(ctx context.Context, productID primitive.ObjectID, version *models.ShopModel3DVersion, failure string) error {
	update := bson.M{"$set": bson.M{"status": models.ShopModel3DIdle, "error": failure}}
	if version != nil {
		update["$push"] = bson.M{"versions": bson.M{"$each": bson.A{version}, "$slice": -shopModel3DMaxVersions}}
	}
	_, err := d.shopModel3D.UpdateOne(ctx, bson.M{"_id": productID}, update)
	return err
}

// UndoShopModel3D retire la dernière version, sauf pendant une génération.
func (d *DB) UndoShopModel3D(ctx context.Context, productID primitive.ObjectID) (bool, error) {
	res, err := d.shopModel3D.UpdateOne(ctx,
		bson.M{"_id": productID, "status": bson.M{"$ne": models.ShopModel3DGenerating}, "versions.1": bson.M{"$exists": true}},
		bson.M{"$pop": bson.M{"versions": 1}},
	)
	if err != nil {
		return false, err
	}
	return res.MatchedCount > 0, nil
}

// PublishShopModel3D fige la scène montrée sur la boutique. Le drapeau de l'article passe en dernier :
// la fiche ne propose la vue 3D qu'une fois la scène disponible.
func (d *DB) PublishShopModel3D(ctx context.Context, productID primitive.ObjectID, scene string, photos []string) error {
	if _, err := d.shopModel3D.UpdateOne(ctx, bson.M{"_id": productID},
		bson.M{"$set": bson.M{"published": scene, "published_photos": photos}}); err != nil {
		return err
	}
	return d.setShopProductModel3D(ctx, productID, true)
}

// UnpublishShopModel3D retire la vue 3D de la boutique. Le drapeau d'abord, pour la même raison.
func (d *DB) UnpublishShopModel3D(ctx context.Context, productID primitive.ObjectID) error {
	if err := d.setShopProductModel3D(ctx, productID, false); err != nil {
		return err
	}
	_, err := d.shopModel3D.UpdateOne(ctx, bson.M{"_id": productID},
		bson.M{"$unset": bson.M{"published": "", "published_photos": ""}})
	return err
}

func (d *DB) setShopProductModel3D(ctx context.Context, productID primitive.ObjectID, published bool) error {
	_, err := d.shopProducts.UpdateOne(ctx, bson.M{"_id": productID},
		bson.M{"$set": bson.M{"model3d_published": published}})
	return err
}

// DeleteShopModel3D efface le travail d'un article et renvoie ses photos, à retirer du disque.
func (d *DB) DeleteShopModel3D(ctx context.Context, productID primitive.ObjectID) ([]string, error) {
	var m models.ShopModel3D
	err := d.shopModel3D.FindOneAndDelete(ctx, bson.M{"_id": productID}).Decode(&m)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return m.Photos, nil
}
