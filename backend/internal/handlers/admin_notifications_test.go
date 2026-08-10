package handlers

import (
	"testing"

	"runapp/internal/models"
)

func TestAdminDisplayName(t *testing.T) {
	cases := []struct {
		name string
		u    models.User
		want string
	}{
		{"prénom + nom", models.User{FirstName: "Mathias", LastName: "Coutant"}, "Mathias COUTANT"},
		{"nom déjà majuscule", models.User{FirstName: "Mathias", LastName: "COUTANT"}, "Mathias COUTANT"},
		{"prénom seul", models.User{FirstName: "Mathias", Email: "m@x.fr"}, "Mathias"},
		{"aucun nom", models.User{Email: "m@x.fr"}, "m@x.fr"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := adminDisplayName(&c.u); got != c.want {
				t.Fatalf("adminDisplayName = %q, want %q", got, c.want)
			}
		})
	}
}

func TestAdminNotificationText(t *testing.T) {
	cases := []struct {
		name      string
		kind      string
		plan      string
		label     string
		wantTitle string
		wantBody  string
	}{
		{
			name:      "offre payante prise après inscription (parcours web)",
			kind:      models.AdminEventPlanActivated,
			plan:      models.PlanPerformance,
			label:     "PERFORMANCE",
			wantTitle: "Nouvelle offre payante",
			wantBody:  "Mathias COUTANT vient de prendre l’offre PERFORMANCE",
		},
		{
			name:      "inscription directement en payant",
			kind:      models.AdminEventSignup,
			plan:      models.PlanStrava,
			label:     "ALLURE",
			wantTitle: "Nouvelle inscription payante",
			wantBody:  "Mathias COUTANT vient de s’inscrire et de prendre l’offre ALLURE",
		},
		{
			name:      "inscription gratuite",
			kind:      models.AdminEventSignup,
			plan:      models.PlanStandard,
			label:     "STANDARD",
			wantTitle: "Nouvelle inscription",
			wantBody:  "Mathias COUTANT vient de s’inscrire — offre STANDARD (gratuite)",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			title, body := adminNotificationText(c.kind, "Mathias COUTANT", c.plan, c.label)
			if title != c.wantTitle {
				t.Errorf("title = %q, want %q", title, c.wantTitle)
			}
			if body != c.wantBody {
				t.Errorf("body = %q, want %q", body, c.wantBody)
			}
		})
	}
}
