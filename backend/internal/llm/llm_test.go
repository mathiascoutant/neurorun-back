package llm

import (
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
)

func roles(msgs []anthropic.BetaMessageParam) []string {
	out := make([]string, len(msgs))
	for i, m := range msgs {
		out[i] = string(m.Role)
	}
	return out
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Le fil du coach : repères de date avant les messages de la personne, puis une
// boucle d'actions suivie d'une note de calendrier à jour.
func TestConvertCoachThread(t *testing.T) {
	msgs := []ChatMessage{
		{Role: "system", Content: "prompt"},
		{Role: "system", Content: "Échanges du lundi"},
		{Role: "user", Content: "salut"},
		{Role: "assistant", Content: "bonjour"},
		{Role: "system", Content: "Nouveau message, aujourd'hui mardi"},
		{Role: "user", Content: "décale ma séance"},
		{Role: "assistant", ToolCalls: []ToolCall{
			{ID: "a", Function: ToolCallFunction{Name: "skip_session", Arguments: `{"week":1}`}},
			{ID: "b", Function: ToolCallFunction{Name: "reschedule_session", Arguments: `{}`}},
		}},
		ToolResultMessage("a", `{"ok":true}`),
		ToolResultMessage("b", `{"ok":true}`),
		{Role: "system", Content: "calendrier à jour"},
	}
	system, out := convert(msgs)

	if len(system) != 1 || system[0].Text != "prompt" {
		t.Fatalf("prompt système : %+v", system)
	}
	want := []string{"user", "assistant", "user", "assistant", "user", "system"}
	if got := roles(out); !equal(got, want) {
		t.Fatalf("rôles : %v, attendu %v", got, want)
	}
	// Le repère de date est glissé dans le message de la personne, avant ses mots.
	if n := len(out[0].Content); n != 2 || out[0].Content[0].OfText == nil || out[0].Content[1].OfText.Text != "salut" {
		t.Fatalf("premier message : %+v", out[0].Content)
	}
	// Les deux retours d'actions partagent un seul message.
	results := out[4].Content
	if len(results) != 2 || results[0].OfToolResult == nil || results[1].OfToolResult == nil {
		t.Fatalf("retours d'actions : %+v", results)
	}
	if len(out[3].Content) != 2 || out[3].Content[0].OfToolUse == nil {
		t.Fatalf("appels d'actions : %+v", out[3].Content)
	}
}

// La réponse renvoyée par le modèle repart telle quelle, réflexion comprise.
func TestConvertKeepsAssistantBlocks(t *testing.T) {
	blocks := []anthropic.BetaContentBlockParamUnion{
		{OfThinking: &anthropic.BetaThinkingBlockParam{Signature: "sig"}},
		anthropic.NewBetaToolUseBlock("a", map[string]any{}, "skip_session"),
	}
	_, out := convert([]ChatMessage{
		{Role: "user", Content: "x"},
		{Role: "assistant", Content: "ignoré", blocks: blocks},
	})
	if len(out) != 2 || len(out[1].Content) != 2 || out[1].Content[0].OfThinking == nil {
		t.Fatalf("blocs de l'assistant : %+v", out)
	}
}

func TestToolParamsSchema(t *testing.T) {
	tools := toolParams([]Tool{NewFunctionTool("t", "d", map[string]any{
		"type":                 "object",
		"properties":           map[string]any{"a": map[string]any{"type": "string"}},
		"required":             []string{"a"},
		"additionalProperties": false,
	})})
	schema := tools[0].OfTool.InputSchema
	if schema.Properties == nil || len(schema.Required) != 1 || schema.ExtraFields["additionalProperties"] != false {
		t.Fatalf("schéma : %+v", schema)
	}
}
