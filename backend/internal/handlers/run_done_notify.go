package handlers

import (
	"context"
	"fmt"
	"strings"
	"time"

	"runapp/internal/models"
	"runapp/internal/push"
)

// runDoneNotificationText : félicite le coureur de la course qu'il vient de
// finir. Le titre salue selon la longueur, le corps reprend les chiffres pour
// se lire sans ouvrir l'app. La note est citée avec les mots de la carte, mais
// une note basse ne gâche pas le bravo : elle devient une invitation à lire
// l'analyse.
func runDoneNotificationText(firstName string, run *models.LiveRun) (title, body string) {
	km := run.DistanceM / 1000
	dist := formatKm(run.DistanceM)
	// Sans prénom, la formule s'en passe : « Bravo, 10 km bouclés ! ».
	who := ""
	if firstName = strings.TrimSpace(firstName); firstName != "" {
		who = " " + firstName
	}

	switch {
	case km >= 40:
		title = fmt.Sprintf("🏅 %s bouclés, chapeau%s !", dist, who)
	case km >= 20:
		title = fmt.Sprintf("🔥 %s dans les jambes, énorme%s !", dist, who)
	case km >= 10:
		title = fmt.Sprintf("💪 Bravo%s, %s bouclés !", who, dist)
	case km >= 5:
		title = fmt.Sprintf("🏃 Bien joué%s, %s au compteur !", who, dist)
	default:
		title = fmt.Sprintf("👏 Bravo%s, chaque sortie compte !", who)
	}

	body = fmt.Sprintf("%s en %s à %s.", dist, formatDuration(run.MovingSec), formatPace(run.AvgPaceSecPerKm))
	if run.GoalSession != nil {
		body = "Séance de ton plan validée ✅ " + body
	}
	if score := scoreTotalOf(run.Score); score != nil {
		switch {
		case *score >= 95:
			body += fmt.Sprintf(" Note de %d/100 : exceptionnelle 🤩", *score)
		case *score >= 90:
			body += fmt.Sprintf(" Note de %d/100, une très bonne course !", *score)
		case *score >= 75:
			body += fmt.Sprintf(" Note de %d/100, belle exécution.", *score)
		default:
			body += fmt.Sprintf(" Ta note : %d/100, touche pour voir ce qui peut progresser.", *score)
		}
	}
	return title, body
}

// notifyRunDone félicite l'auteur sur ses propres appareils. Mêmes règles que
// pour les amis : une vraie sortie, finie il y a peu. Une course de montre
// arrive sur le téléphone sans que l'app soit ouverte : c'est souvent la
// première nouvelle que le coureur en a.
func (h *Handlers) notifyRunDone(u *models.User, run *models.LiveRun) {
	if !shouldNotifyFriendsOfRun(run, time.Now()) {
		return
	}
	title, body := runDoneNotificationText(u.FirstName, run)
	userID := u.ID
	msg := push.Message{
		Title: title,
		Body:  body,
		Data:  map[string]any{"kind": "run_done", "run_id": run.ID.Hex()},
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), notifyTimeout)
		defer cancel()
		h.sendToUser(ctx, userID, msg)
	}()
}
