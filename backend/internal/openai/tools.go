package openai

import "context"

// Tool décrit une action que le modèle peut demander (function calling).
type Tool struct {
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}

// ToolFunction : Parameters est un JSON Schema (map[string]any suffit).
type ToolFunction struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Parameters  any    `json:"parameters"`
}

// ToolCall est une action demandée par le modèle. Arguments est du JSON brut :
// c'est à l'appelant de le valider, un modèle peut produire n'importe quoi.
type ToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function ToolCallFunction `json:"function"`
}

type ToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// NewFunctionTool construit un outil de type function.
func NewFunctionTool(name, description string, parameters any) Tool {
	return Tool{
		Type:     "function",
		Function: ToolFunction{Name: name, Description: description, Parameters: parameters},
	}
}

// ToolResultMessage formate le retour d'une action exécutée, à renvoyer au modèle
// pour qu'il rédige sa réponse en connaissance de ce qui s'est réellement passé.
func ToolResultMessage(callID, payload string) ChatMessage {
	return ChatMessage{Role: "tool", ToolCallID: callID, Content: payload}
}

type toolChatRequest struct {
	Model      string        `json:"model"`
	Messages   []ChatMessage `json:"messages"`
	Tools      []Tool        `json:"tools,omitempty"`
	ToolChoice string        `json:"tool_choice,omitempty"`
}

// ChatMessagesWithTools renvoie le message d'assistant tel quel : soit du texte,
// soit une liste d'actions à exécuter avant de relancer l'appel avec leurs retours.
func (c *Client) ChatMessagesWithTools(
	ctx context.Context,
	messages []ChatMessage,
	tools []Tool,
) (ChatMessage, error) {
	return c.chatWithToolChoice(ctx, messages, tools, "auto")
}

// ChatMessagesNoMoreTools réclame une réponse en texte. Les outils restent déclarés
// pour que l'historique, qui contient déjà des appels et leurs retours, reste
// cohérent aux yeux de l'API — seul leur usage est fermé.
func (c *Client) ChatMessagesNoMoreTools(
	ctx context.Context,
	messages []ChatMessage,
	tools []Tool,
) (ChatMessage, error) {
	return c.chatWithToolChoice(ctx, messages, tools, "none")
}

func (c *Client) chatWithToolChoice(
	ctx context.Context,
	messages []ChatMessage,
	tools []Tool,
	choice string,
) (ChatMessage, error) {
	body := toolChatRequest{Model: c.Model, Messages: messages, Tools: tools}
	if len(tools) > 0 {
		body.ToolChoice = choice
	}
	cr, err := c.postCompletion(ctx, body)
	if err != nil {
		return ChatMessage{}, err
	}
	return cr.Choices[0].Message, nil
}
