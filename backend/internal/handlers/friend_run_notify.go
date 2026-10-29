package handlers

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"strconv"
	"strings"
	"time"

	"runapp/internal/goalcalendar"
	"runapp/internal/models"
	"runapp/internal/push"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

const (
	// En dessous d'un kilomètre, ce n'est pas une sortie à saluer : essai de GPS,
	// départ arrêté par erreur, échauffement enregistré seul.
	minFriendRunNotifyM = 1000
	// Une course de montre restée en file plusieurs jours n'est plus « tout juste
	// courue » : passé ce délai depuis la fin, les amis ne sont pas prévenus.
	maxFriendRunNotifyAge = 12 * time.Hour
)

// shouldNotifyFriendsOfRun : seule une vraie sortie, terminée il y a peu, mérite de
// faire sonner le téléphone des amis.
func shouldNotifyFriendsOfRun(run *models.LiveRun, now time.Time) bool {
	if run == nil || run.DistanceM < minFriendRunNotifyM {
		return false
	}
	return now.Sub(liveRunEnd(run)) <= maxFriendRunNotifyAge
}

// liveRunEnd : fin GPS si la montre l'a fournie, sinon départ + durée totale.
func liveRunEnd(run *models.LiveRun) time.Time {
	if run.GpsEndTsMs > 0 {
		return time.UnixMilli(run.GpsEndTsMs).UTC()
	}
	return goalcalendar.LiveRunStart(*run).Add(time.Duration(run.WallSec * float64(time.Second)))
}

// friendRunRecipients : les amis à prévenir, sans l'auteur ni doublon — une amitié
// mal formée en base ne doit ni notifier deux fois, ni renvoyer la notif à soi-même.
func friendRunRecipients(authorID primitive.ObjectID, friendIDs []primitive.ObjectID) []primitive.ObjectID {
	seen := make(map[primitive.ObjectID]bool, len(friendIDs))
	out := make([]primitive.ObjectID, 0, len(friendIDs))
	for _, id := range friendIDs {
		if id == authorID || id.IsZero() || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

// friendRunNotificationText : le titre dit qui a couru et combien, le corps salue la
// sortie selon sa longueur et invite à aller l'encourager. Une bonne note est citée ;
// une note moyenne est tue, la notif n'est pas là pour juger.
func friendRunNotificationText(name string, distanceM float64, score *int) (title, body string) {
	km := distanceM / 1000
	dist := formatRoundedKm(distanceM)

	var praise string
	switch {
	case km >= 40:
		title = "🏅 " + name + " vient de courir " + dist + " !"
		praise = "Une sortie de marathonien, chapeau !"
	case km >= 20:
		title = "🔥 " + name + " vient d’avaler " + dist
		praise = "Une grosse sortie dans les jambes."
	case km >= 10:
		title = "💪 " + name + " vient de courir " + dist
		praise = "Une vraie sortie, bien menée."
	case km >= 5:
		title = "🏃 " + name + " vient de courir " + dist
		praise = "Belle sortie !"
	default:
		title = "🏃 " + name + " vient de courir " + dist
		praise = "Chaque sortie compte."
	}

	if score != nil {
		switch {
		case *score >= 90:
			praise = fmt.Sprintf("Note de %d/100, exceptionnelle !", *score)
		case *score >= 75:
			praise = fmt.Sprintf("%s Note de %d/100.", praise, *score)
		}
	}
	return title, praise + " Va l’encourager 👏"
}

// formatRoundedKm : « 5 km », « 7,5 km », « 21 km ». Un dixième sous 10 km, où il
// se remarque ; le kilomètre au-delà, où il ne dit plus rien.
func formatRoundedKm(distanceM float64) string {
	km := distanceM / 1000
	if km >= 10 {
		return strconv.Itoa(int(math.Round(km))) + " km"
	}
	s := strconv.FormatFloat(math.Round(km*10)/10, 'f', 1, 64)
	s = strings.TrimSuffix(s, ".0")
	return strings.Replace(s, ".", ",", 1) + " km"
}

// notifyFriendsOfRun prévient en arrière-plan les amis qui ont un appareil
// enregistré : la réponse à la montre ne doit ni attendre Expo, ni échouer à cause de lui.
func (h *Handlers) notifyFriendsOfRun(author *models.User, run *models.LiveRun) {
	if !shouldNotifyFriendsOfRun(run, time.Now()) {
		return
	}
	authorID := author.ID
	name := friendDisplayName(author)
	title, body := friendRunNotificationText(name, run.DistanceM, scoreTotalOf(run.Score))
	msg := push.Message{
		Title: title,
		Body:  body,
		// Ce qu'attend le détail d'une course d'ami : l'id, et le prénom pour le titre.
		Data: map[string]any{
			"kind":        "friend_run",
			"run_id":      run.ID.Hex(),
			"friend_id":   authorID.Hex(),
			"friend_name": name,
		},
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), notifyTimeout)
		defer cancel()
		friendIDs, err := h.db.ListFriendIDs(ctx, authorID)
		if err != nil {
			log.Printf("push course d'ami: amis: %v", err)
			return
		}
		recipients := friendRunRecipients(authorID, friendIDs)
		if len(recipients) == 0 {
			return
		}
		tokens, err := h.db.ListPushTokensByUsers(ctx, recipients)
		if err != nil {
			log.Printf("push course d'ami: jetons: %v", err)
			return
		}
		h.sendToTokens(ctx, tokens, msg)
	}()
}

// sendToTokens envoie un même message à plusieurs appareils et purge ceux que Expo
// signale comme morts.
func (h *Handlers) sendToTokens(ctx context.Context, tokens []string, m push.Message) {
	if len(tokens) == 0 {
		return
	}
	invalid, err := h.pushClient.Send(ctx, tokens, m)
	if err != nil && !errors.Is(err, push.ErrNotConfigured) {
		log.Printf("push social: envoi: %v", err)
	}
	if len(invalid) > 0 {
		_ = h.db.DeletePushTokens(ctx, invalid)
	}
}
