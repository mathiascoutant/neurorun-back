package llm

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
)

// Low renvoie le même client en effort « low » : pour les petites tâches (résumer
// des chiffres, découper une séance en étapes) où réfléchir longtemps ne change rien
// au résultat mais se paie.
func (c *Client) Low() *Client {
	cp := *c
	cp.Effort = anthropic.BetaOutputConfigEffortLow
	return &cp
}

// maxSearchResumes : une recherche que le serveur met en pause est relancée telle
// quelle ; au-delà, on rend ce qu'on a plutôt que de boucler.
const maxSearchResumes = 3

// Search répond en s'appuyant sur une recherche web faite par le modèle (outil
// hébergé par Anthropic : rien à exécuter de notre côté). maxUses borne le nombre
// de recherches, donc le coût.
func (c *Client) Search(ctx context.Context, systemPrompt, userMessage string, maxUses int64) (string, error) {
	params := anthropic.BetaMessageNewParams{
		Model:        anthropic.Model(c.Model),
		MaxTokens:    16000,
		System:       []anthropic.BetaTextBlockParam{{Text: systemPrompt}},
		Messages:     []anthropic.BetaMessageParam{anthropic.NewBetaUserMessage(anthropic.NewBetaTextBlock(userMessage))},
		OutputConfig: anthropic.BetaOutputConfigParam{Effort: c.Effort},
		Tools: []anthropic.BetaToolUnionParam{{
			OfWebSearchTool20260209: &anthropic.BetaWebSearchTool20260209Param{MaxUses: anthropic.Int(maxUses)},
		}},
	}

	for attempt := 0; ; attempt++ {
		stream := c.api.Beta.Messages.NewStreaming(ctx, params)
		message := anthropic.BetaMessage{}
		for stream.Next() {
			if err := message.Accumulate(stream.Current()); err != nil {
				return "", err
			}
		}
		if err := stream.Err(); err != nil {
			return "", err
		}
		log.Printf("llm recherche : %s, jetons en entrée %d, %d en sortie, arrêt %s",
			message.Model, message.Usage.InputTokens, message.Usage.OutputTokens, message.StopReason)

		switch message.StopReason {
		case anthropic.BetaStopReasonRefusal:
			return "", fmt.Errorf("%w : %s", ErrRefused, message.StopDetails.Explanation)
		case anthropic.BetaStopReasonPauseTurn:
			if attempt < maxSearchResumes {
				params.Messages = append(params.Messages, message.ToParam())
				continue
			}
		}
		var text strings.Builder
		for _, block := range message.Content {
			if t, ok := block.AsAny().(anthropic.BetaTextBlock); ok {
				text.WriteString(t.Text)
			}
		}
		if text.Len() == 0 {
			return "", errors.New("llm : recherche sans réponse")
		}
		return text.String(), nil
	}
}
