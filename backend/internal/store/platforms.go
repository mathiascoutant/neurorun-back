package store

import (
	"context"
	"sort"
	"time"

	"runapp/internal/models"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// PlatformAccount : un compte, vu sous l’angle des appareils depuis lesquels il s’est connecté.
type PlatformAccount struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	Name  string `json:"name,omitempty"`
	// Platforms : plateformes distinctes vues sur ce compte, ordre stable.
	Platforms []string `json:"platforms"`
	// FirstSeen / LastSeen : première et dernière session ouverte, par plateforme (RFC3339).
	FirstSeen map[string]time.Time `json:"first_seen"`
	LastSeen  map[string]time.Time `json:"last_seen"`
	// Sessions : nombre de sessions ouvertes, par plateforme.
	Sessions map[string]int64 `json:"sessions"`
}

// DeviceSample : un libellé d’appareil brut et son nombre d’occurrences.
type DeviceSample struct {
	Device string `json:"device"`
	Count  int64  `json:"count"`
}

// PlatformReport : ce que la console affiche dans l’onglet Plateformes.
type PlatformReport struct {
	Accounts []PlatformAccount `json:"accounts"`
	// AccountsByPlatform : nombre de comptes ayant au moins une session sur la plateforme.
	AccountsByPlatform map[string]int64 `json:"accounts_by_platform"`
	// SamplesByPlatform : libellés bruts les plus fréquents, par plateforme. Sert à vérifier
	// le classement sur de vraies données plutôt qu’à lui faire confiance.
	SamplesByPlatform map[string][]DeviceSample `json:"samples_by_platform"`
	// UsersTotal / UsersWithoutSession : un compte sans aucune session longue enregistrée
	// n’apparaît nulle part ci-dessus. Le dire évite de lire les totaux de travers.
	UsersTotal          int64 `json:"users_total"`
	UsersWithoutSession int64 `json:"users_without_session"`
}

// deviceRow : une ligne de l’agrégation (un compte, un libellé d’appareil).
type deviceRow struct {
	UserID primitive.ObjectID `bson:"user_id"`
	Device string             `bson:"device"`
	First  time.Time          `bson:"first"`
	Last   time.Time          `bson:"last"`
	Count  int64              `bson:"count"`
}

const platformSamplesPerPlatform = 4

// PlatformsReport agrège les sessions longues par compte et par plateforme.
//
// On lit tous les jetons, y compris expirés et révoqués : la question est « ce compte s’est-il
// déjà connecté depuis un iPhone », pas « a-t-il une session valide en ce moment ».
func (d *DB) PlatformsReport(ctx context.Context) (PlatformReport, error) {
	cur, err := d.refreshTokens.Aggregate(ctx, []bson.M{
		{"$group": bson.M{
			"_id":   bson.M{"user_id": "$user_id", "device": "$device"},
			"first": bson.M{"$min": "$created_at"},
			"last":  bson.M{"$max": "$created_at"},
			"count": bson.M{"$sum": 1},
		}},
		{"$project": bson.M{
			"_id":     0,
			"user_id": "$_id.user_id",
			"device":  "$_id.device",
			"first":   1,
			"last":    1,
			"count":   1,
		}},
	})
	if err != nil {
		return PlatformReport{}, err
	}
	defer cur.Close(ctx)

	byUser := map[primitive.ObjectID]*PlatformAccount{}
	deviceCounts := map[string]map[string]int64{}

	for cur.Next(ctx) {
		var row deviceRow
		if err := cur.Decode(&row); err != nil {
			return PlatformReport{}, err
		}
		platform := models.PlatformFromDevice(row.Device)

		if deviceCounts[platform] == nil {
			deviceCounts[platform] = map[string]int64{}
		}
		deviceCounts[platform][row.Device] += row.Count

		acc := byUser[row.UserID]
		if acc == nil {
			acc = &PlatformAccount{
				ID:        row.UserID.Hex(),
				FirstSeen: map[string]time.Time{},
				LastSeen:  map[string]time.Time{},
				Sessions:  map[string]int64{},
			}
			byUser[row.UserID] = acc
		}
		acc.Sessions[platform] += row.Count
		if cur, ok := acc.FirstSeen[platform]; !ok || row.First.Before(cur) {
			acc.FirstSeen[platform] = row.First
		}
		if cur, ok := acc.LastSeen[platform]; !ok || row.Last.After(cur) {
			acc.LastSeen[platform] = row.Last
		}
	}
	if err := cur.Err(); err != nil {
		return PlatformReport{}, err
	}

	if err := d.fillPlatformAccountIdentities(ctx, byUser); err != nil {
		return PlatformReport{}, err
	}

	report := PlatformReport{
		Accounts:           make([]PlatformAccount, 0, len(byUser)),
		AccountsByPlatform: map[string]int64{},
		SamplesByPlatform:  map[string][]DeviceSample{},
	}
	for _, acc := range byUser {
		acc.Platforms = orderedPlatforms(acc.Sessions)
		for _, p := range acc.Platforms {
			report.AccountsByPlatform[p]++
		}
		report.Accounts = append(report.Accounts, *acc)
	}
	/* Les comptes iOS d’abord, puis les plus récents : c’est la liste qu’on vient lire. */
	sort.Slice(report.Accounts, func(i, j int) bool {
		a, b := report.Accounts[i], report.Accounts[j]
		ai, bi := a.Sessions[models.PlatformIOS] > 0, b.Sessions[models.PlatformIOS] > 0
		if ai != bi {
			return ai
		}
		at, bt := latestSeen(a.LastSeen), latestSeen(b.LastSeen)
		if !at.Equal(bt) {
			return at.After(bt)
		}
		return a.Email < b.Email
	})

	for platform, devices := range deviceCounts {
		report.SamplesByPlatform[platform] = topDeviceSamples(devices, platformSamplesPerPlatform)
	}

	total, err := d.CountUsers(ctx)
	if err != nil {
		return PlatformReport{}, err
	}
	report.UsersTotal = total
	if n := total - int64(len(byUser)); n > 0 {
		report.UsersWithoutSession = n
	}
	return report, nil
}

// fillPlatformAccountIdentities complète email et nom en une seule lecture de `users`.
func (d *DB) fillPlatformAccountIdentities(
	ctx context.Context,
	byUser map[primitive.ObjectID]*PlatformAccount,
) error {
	if len(byUser) == 0 {
		return nil
	}
	ids := make([]primitive.ObjectID, 0, len(byUser))
	for id := range byUser {
		ids = append(ids, id)
	}
	cur, err := d.users.Find(ctx,
		bson.M{"_id": bson.M{"$in": ids}},
		options.Find().SetProjection(bson.M{"email": 1, "first_name": 1, "last_name": 1}),
	)
	if err != nil {
		return err
	}
	defer cur.Close(ctx)
	for cur.Next(ctx) {
		var u struct {
			ID        primitive.ObjectID `bson:"_id"`
			Email     string             `bson:"email"`
			FirstName string             `bson:"first_name"`
			LastName  string             `bson:"last_name"`
		}
		if err := cur.Decode(&u); err != nil {
			return err
		}
		acc := byUser[u.ID]
		if acc == nil {
			continue
		}
		acc.Email = u.Email
		acc.Name = models.DisplayName(u.FirstName, u.LastName, u.Email)
	}
	if err := cur.Err(); err != nil {
		return err
	}
	/* Un compte supprimé peut laisser des jetons derrière lui : on ne l’affiche pas. */
	for id, acc := range byUser {
		if acc.Email == "" {
			delete(byUser, id)
		}
	}
	return nil
}

func orderedPlatforms(sessions map[string]int64) []string {
	out := make([]string, 0, len(sessions))
	for _, p := range models.PlatformOrder {
		if sessions[p] > 0 {
			out = append(out, p)
		}
	}
	return out
}

func latestSeen(m map[string]time.Time) time.Time {
	var out time.Time
	for _, t := range m {
		if t.After(out) {
			out = t
		}
	}
	return out
}

func topDeviceSamples(devices map[string]int64, limit int) []DeviceSample {
	out := make([]DeviceSample, 0, len(devices))
	for device, n := range devices {
		out = append(out, DeviceSample{Device: device, Count: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Device < out[j].Device
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// ListDeviceLabelsForUser : libellés d’appareil des sessions du compte, hors celle passée en
// `exclude`. Sert à savoir si une session iOS est la première du compte.
func (d *DB) ListDeviceLabelsForUser(
	ctx context.Context,
	userID primitive.ObjectID,
	exclude primitive.ObjectID,
) ([]string, error) {
	filter := bson.M{"user_id": userID}
	if !exclude.IsZero() {
		filter["_id"] = bson.M{"$ne": exclude}
	}
	cur, err := d.refreshTokens.Find(ctx, filter, options.Find().SetProjection(bson.M{"device": 1}))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var out []string
	for cur.Next(ctx) {
		var row struct {
			Device string `bson:"device"`
		}
		if err := cur.Decode(&row); err != nil {
			return nil, err
		}
		out = append(out, row.Device)
	}
	return out, cur.Err()
}
