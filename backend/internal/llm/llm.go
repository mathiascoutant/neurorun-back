// Package llm fait écrire Claude pour le coach : plans, avis de faisabilité, réponses
// du chat et actions sur le calendrier.
//
// Les handlers parlent en messages simples (system / user / assistant / tool) ; ce
// paquet les traduit vers l'API Messages d'Anthropic, qui range les choses autrement :
// un prompt système à part, les retours d'actions dans un message « user », et des
// blocs de réflexion à rendre tels quels pendant une boucle d'actions.
package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// ErrRefused : le modèle a décliné la demande (filtres de sécurité).
var ErrRefused = errors.New("demande refusée par le modèle")

// maxTokens : un plan de 52 semaines dépasse largement 16 000 jetons. Tout passe en
// flux, donc ce plafond haut ne fait pas tomber les requêtes en délai dépassé ; on ne
// paie que ce qui est réellement écrit.
const maxTokens = 64000

// ChatMessage : un message de la conversation, dans la forme qu'utilisent les handlers.
type ChatMessage struct {
	Role    string
	Content string
	// ToolCalls : actions demandées par le modèle (message d'assistant).
	ToolCalls []ToolCall
	// ToolCallID : identifiant de l'action à laquelle ce message répond (rôle "tool").
	ToolCallID string
	// blocks : la réponse du modèle telle quelle, réflexion comprise. Pendant une
	// boucle d'actions, l'API exige de la recevoir inchangée au tour suivant.
	blocks []anthropic.BetaContentBlockParamUnion
}

// Tool décrit une action que le modèle peut demander. Parameters est un JSON Schema.
type Tool struct {
	Name        string
	Description string
	Parameters  map[string]any
}

// ToolCall est une action demandée par le modèle. Arguments est du JSON brut :
// c'est à l'appelant de le valider, un modèle peut produire n'importe quoi.
type ToolCall struct {
	ID       string
	Function ToolCallFunction
}

type ToolCallFunction struct {
	Name      string
	Arguments string
}

// NewFunctionTool construit un outil à partir de son schéma JSON.
func NewFunctionTool(name, description string, parameters map[string]any) Tool {
	return Tool{Name: name, Description: description, Parameters: parameters}
}

// ToolResultMessage formate le retour d'une action exécutée, à renvoyer au modèle
// pour qu'il rédige sa réponse en connaissance de ce qui s'est réellement passé.
func ToolResultMessage(callID, payload string) ChatMessage {
	return ChatMessage{Role: "tool", ToolCallID: callID, Content: payload}
}

type Client struct {
	api    anthropic.Client
	Model  string
	Effort anthropic.BetaOutputConfigEffort
}

// New : effort « medium », le défaut de Claude Opus 5.5. Assez pour raisonner sur un
// calendrier ou un plan, sans payer la réflexion longue réservée aux problèmes durs.
func New(apiKey, model string) *Client {
	return &Client{
		api:    anthropic.NewClient(option.WithAPIKey(apiKey)),
		Model:  model,
		Effort: anthropic.BetaOutputConfigEffortMedium,
	}
}

// Chat envoie un unique tour system + user.
func (c *Client) Chat(ctx context.Context, systemPrompt, userMessage string) (string, error) {
	return c.ChatMessages(ctx, []ChatMessage{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userMessage},
	})
}

// ChatMessages envoie une conversation complète et renvoie le texte de la réponse.
func (c *Client) ChatMessages(ctx context.Context, messages []ChatMessage) (string, error) {
	msg, err := c.send(ctx, messages, nil, nil, nil)
	if err != nil {
		return "", err
	}
	return msg.Content, nil
}

// ChatMessagesStream diffuse la réponse au fil de sa génération : `onDelta` reçoit
// chaque fragment de texte dès qu'il arrive, et le texte complet est renvoyé à la fin.
//
// C'est ce qui permet de suivre une génération longue sur des faits — ce qui est
// écrit — au lieu de deviner une progression à partir du temps écoulé.
func (c *Client) ChatMessagesStream(ctx context.Context, messages []ChatMessage, onDelta func(string)) (string, error) {
	msg, err := c.send(ctx, messages, nil, nil, onDelta)
	if err != nil {
		return "", err
	}
	return msg.Content, nil
}

// ChatMessagesWithTools renvoie le message d'assistant tel quel : soit du texte,
// soit une liste d'actions à exécuter avant de relancer l'appel avec leurs retours.
func (c *Client) ChatMessagesWithTools(ctx context.Context, messages []ChatMessage, tools []Tool) (ChatMessage, error) {
	auto := anthropic.BetaToolChoiceUnionParam{OfAuto: &anthropic.BetaToolChoiceAutoParam{}}
	return c.send(ctx, messages, tools, &auto, nil)
}

// ChatMessagesNoMoreTools réclame une réponse en texte. Les outils restent déclarés
// pour que l'historique, qui contient déjà des appels et leurs retours, reste
// cohérent aux yeux de l'API — seul leur usage est fermé.
func (c *Client) ChatMessagesNoMoreTools(ctx context.Context, messages []ChatMessage, tools []Tool) (ChatMessage, error) {
	none := anthropic.BetaToolChoiceUnionParam{OfNone: &anthropic.BetaToolChoiceNoneParam{}}
	return c.send(ctx, messages, tools, &none, nil)
}

// supportsFallback : seuls ces modèles ont un repli côté serveur quand leurs filtres
// se déclenchent à tort ; sans lui, une demande refusée s'arrête net.
func supportsFallback(model string) bool {
	switch model {
	case "claude-opus-5-5", "claude-opus-5", "claude-fable-5-1", "claude-fable-5-2", "claude-sonnet-5-5":
		return true
	}
	return false
}

// send passe toujours par le flux, même quand l'appelant n'attend que le résultat :
// un plan long dépasserait sinon le délai d'un appel simple.
func (c *Client) send(
	ctx context.Context,
	messages []ChatMessage,
	tools []Tool,
	choice *anthropic.BetaToolChoiceUnionParam,
	onDelta func(string),
) (ChatMessage, error) {
	system, msgs := convert(messages)
	if len(msgs) == 0 {
		return ChatMessage{}, errors.New("llm : aucun message à envoyer")
	}
	params := anthropic.BetaMessageNewParams{
		Model:        anthropic.Model(c.Model),
		MaxTokens:    maxTokens,
		System:       system,
		Messages:     msgs,
		OutputConfig: anthropic.BetaOutputConfigParam{Effort: c.Effort},
		// Le prompt système (activités Strava, plan, calendrier) est repris à chaque
		// tour de la boucle d'actions : en cache, il coûte dix fois moins cher.
		CacheControl: anthropic.NewBetaCacheControlEphemeralParam(),
	}
	if len(tools) > 0 {
		params.Tools = toolParams(tools)
		if choice != nil {
			params.ToolChoice = *choice
		}
	}
	if supportsFallback(c.Model) {
		params.Betas = []anthropic.AnthropicBeta{anthropic.AnthropicBetaServerSideFallback2026_07_01}
		params.Fallbacks = anthropic.BetaFallbacksParamOfDefault()
	}

	stream := c.api.Beta.Messages.NewStreaming(ctx, params)
	message := anthropic.BetaMessage{}
	for stream.Next() {
		event := stream.Current()
		if err := message.Accumulate(event); err != nil {
			return ChatMessage{}, err
		}
		if onDelta == nil {
			continue
		}
		if delta, ok := event.AsAny().(anthropic.BetaRawContentBlockDeltaEvent); ok {
			if text, ok := delta.Delta.AsAny().(anthropic.BetaTextDelta); ok && text.Text != "" {
				onDelta(text.Text)
			}
		}
	}
	if err := stream.Err(); err != nil {
		return ChatMessage{}, err
	}
	log.Printf("llm : %s, jetons en entrée %d + %d écrits en cache + %d lus en cache, %d en sortie, arrêt %s",
		message.Model, message.Usage.InputTokens, message.Usage.CacheCreationInputTokens,
		message.Usage.CacheReadInputTokens, message.Usage.OutputTokens, message.StopReason)

	if message.StopReason == anthropic.BetaStopReasonRefusal {
		return ChatMessage{}, fmt.Errorf("%w : %s", ErrRefused, message.StopDetails.Explanation)
	}

	out := ChatMessage{Role: "assistant", blocks: message.ToParam().Content}
	var text strings.Builder
	for _, block := range message.Content {
		switch b := block.AsAny().(type) {
		case anthropic.BetaTextBlock:
			text.WriteString(b.Text)
		case anthropic.BetaToolUseBlock:
			args, err := json.Marshal(b.Input)
			if err != nil {
				args = []byte("{}")
			}
			out.ToolCalls = append(out.ToolCalls, ToolCall{
				ID:       b.ID,
				Function: ToolCallFunction{Name: b.Name, Arguments: string(args)},
			})
		}
	}
	out.Content = text.String()
	if out.Content == "" && len(out.ToolCalls) == 0 {
		return ChatMessage{}, errors.New("llm : réponse vide")
	}
	return out, nil
}

func toolParams(tools []Tool) []anthropic.BetaToolUnionParam {
	out := make([]anthropic.BetaToolUnionParam, 0, len(tools))
	for _, t := range tools {
		schema := anthropic.BetaToolInputSchemaParam{ExtraFields: map[string]any{}}
		for k, v := range t.Parameters {
			switch k {
			case "type":
			case "properties":
				schema.Properties = v
			case "required":
				if req, ok := v.([]string); ok {
					schema.Required = req
				}
			default:
				schema.ExtraFields[k] = v
			}
		}
		out = append(out, anthropic.BetaToolUnionParam{OfTool: &anthropic.BetaToolParam{
			Name:        t.Name,
			Description: anthropic.String(t.Description),
			InputSchema: schema,
		}})
	}
	return out
}

// convert range la conversation comme l'API l'attend.
//
//   - Le message système en tête devient le prompt système.
//   - Plus loin, un message système n'est accepté qu'après un message « user » : il
//     sert alors de note d'opérateur (calendrier rafraîchi après une action). Placé
//     avant un message de la personne (repères de date de l'historique), il est
//     glissé en tête de ce message, balisé pour ne pas être pris pour ses mots.
//   - Les retours d'actions vont, ensemble, dans un seul message « user ».
func convert(messages []ChatMessage) ([]anthropic.BetaTextBlockParam, []anthropic.BetaMessageParam) {
	var system []anthropic.BetaTextBlockParam
	var out []anthropic.BetaMessageParam
	var pending []string

	// Seul le premier message est le prompt système : un message système qui le suit
	// directement (repère de date du fil) se rapporte au message de la personne d'après.
	i := 0
	if len(messages) > 0 && messages[0].Role == "system" {
		if s := strings.TrimSpace(messages[0].Content); s != "" {
			system = append(system, anthropic.BetaTextBlockParam{Text: s})
		}
		i = 1
	}

	lastRole := func() anthropic.BetaMessageParamRole {
		if len(out) == 0 {
			return ""
		}
		return out[len(out)-1].Role
	}

	for ; i < len(messages); i++ {
		m := messages[i]
		switch m.Role {
		case "system":
			note := strings.TrimSpace(m.Content)
			if note == "" {
				continue
			}
			nextIsUser := i+1 < len(messages) && messages[i+1].Role == "user"
			if !nextIsUser && lastRole() == anthropic.BetaMessageParamRoleUser {
				out = append(out, anthropic.BetaMessageParam{
					Role:    anthropic.BetaMessageParamRoleSystem,
					Content: []anthropic.BetaContentBlockParamUnion{anthropic.NewBetaTextBlock(note)},
				})
				continue
			}
			pending = append(pending, note)

		case "user":
			blocks := make([]anthropic.BetaContentBlockParamUnion, 0, len(pending)+1)
			for _, note := range pending {
				blocks = append(blocks, anthropic.NewBetaTextBlock("<contexte>\n"+note+"\n</contexte>"))
			}
			pending = nil
			if s := strings.TrimSpace(m.Content); s != "" {
				blocks = append(blocks, anthropic.NewBetaTextBlock(s))
			}
			if len(blocks) > 0 {
				out = append(out, anthropic.NewBetaUserMessage(blocks...))
			}

		case "assistant":
			blocks := m.blocks
			if blocks == nil {
				if s := strings.TrimSpace(m.Content); s != "" {
					blocks = append(blocks, anthropic.NewBetaTextBlock(s))
				}
				for _, call := range m.ToolCalls {
					var input any = map[string]any{}
					_ = json.Unmarshal([]byte(call.Function.Arguments), &input)
					blocks = append(blocks, anthropic.NewBetaToolUseBlock(call.ID, input, call.Function.Name))
				}
			}
			if len(blocks) > 0 {
				out = append(out, anthropic.BetaMessageParam{Role: anthropic.BetaMessageParamRoleAssistant, Content: blocks})
			}

		case "tool":
			result := anthropic.NewBetaToolResultBlock(m.ToolCallID, m.Content, false)
			// Les retours d'un même tour d'actions partagent un seul message.
			if n := len(out); n > 0 && out[n-1].Role == anthropic.BetaMessageParamRoleUser && isToolResults(out[n-1]) {
				out[n-1].Content = append(out[n-1].Content, result)
				continue
			}
			out = append(out, anthropic.NewBetaUserMessage(result))
		}
	}

	// Une note restée sans message de la personne après elle complète le prompt système.
	for _, note := range pending {
		system = append(system, anthropic.BetaTextBlockParam{Text: note})
	}
	return system, out
}

func isToolResults(m anthropic.BetaMessageParam) bool {
	for _, b := range m.Content {
		if b.OfToolResult == nil {
			return false
		}
	}
	return len(m.Content) > 0
}
