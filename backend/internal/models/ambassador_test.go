package models

import (
	"strings"
	"testing"
	"time"
)

// Le statut affiché se déduit des signatures : un champ resté en arrière ne doit pas
// faire croire qu’un contrat signé des deux côtés attend encore quelqu’un.
func TestAmbassadorEffectiveStatus(t *testing.T) {
	sig := &Signature{FullName: "Gaël BIZOT", Mention: AmbassadorMention, SignedAt: time.Now()}
	cases := []struct {
		name string
		amb  Ambassador
		want string
	}{
		{"désigné", Ambassador{Status: AmbassadorStatusPending}, AmbassadorStatusPending},
		{"signé par l’ambassadeur", Ambassador{Status: AmbassadorStatusPending, AmbassadorSignature: sig}, AmbassadorStatusSigned},
		{
			"contresigné",
			Ambassador{Status: AmbassadorStatusPending, AmbassadorSignature: sig, CompanySignature: sig},
			AmbassadorStatusActive,
		},
		{
			"terminé malgré les signatures",
			Ambassador{Status: AmbassadorStatusEnded, AmbassadorSignature: sig, CompanySignature: sig},
			AmbassadorStatusEnded,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.amb.EffectiveStatus(); got != tc.want {
				t.Fatalf("statut = %q, attendu %q", got, tc.want)
			}
		})
	}
}

// Reprendre un contrat terminé le remet là où les signatures l’avaient laissé.
func TestAmbassadorSignatureStatusIgnoresEnded(t *testing.T) {
	sig := &Signature{SignedAt: time.Now()}
	amb := Ambassador{Status: AmbassadorStatusEnded, AmbassadorSignature: sig, CompanySignature: sig}
	if got := amb.SignatureStatus(); got != AmbassadorStatusActive {
		t.Fatalf("statut des signatures = %q, attendu %q", got, AmbassadorStatusActive)
	}
}

func TestNormalizeInstagram(t *testing.T) {
	cases := map[string]string{
		"@gaeltrailrunning":                           "gaeltrailrunning",
		"  gaeltrailrunning ":                         "gaeltrailrunning",
		"https://www.instagram.com/gaeltrailrunning/": "gaeltrailrunning",
		"instagram.com/gaeltrailrunning?hl=fr":        "gaeltrailrunning",
		"":                                            "",
	}
	for in, want := range cases {
		if got := NormalizeInstagram(in); got != want {
			t.Fatalf("NormalizeInstagram(%q) = %q, attendu %q", in, got, want)
		}
	}
}

// La mention est recopiée à la main : la casse et les accents ne doivent pas bloquer une
// signature, mais une autre phrase doit être refusée.
func TestMentionMatches(t *testing.T) {
	ok := []string{"Lu et approuvé", "lu et approuve", "  LU ET APPROUVÉ  ", "Lu  et   approuvé"}
	for _, s := range ok {
		if !MentionMatches(s) {
			t.Fatalf("mention refusée à tort : %q", s)
		}
	}
	ko := []string{"", "lu", "approuvé", "lu et approuvé le contrat"}
	for _, s := range ko {
		if MentionMatches(s) {
			t.Fatalf("mention acceptée à tort : %q", s)
		}
	}
}

// Le contrat est le texte qui engage : la remise du code doit y figurer, et la version
// doit accompagner chaque signature.
func TestBuildAmbassadorContractCarriesDiscount(t *testing.T) {
	c := BuildAmbassadorContract("30 %")
	if c.Version != AmbassadorContractVersion {
		t.Fatalf("version = %q", c.Version)
	}
	var found bool
	for _, a := range c.Articles {
		for _, b := range a.Blocks {
			for _, bullet := range b.Bullets {
				if strings.Contains(bullet, "30 %") {
					found = true
				}
			}
		}
	}
	if !found {
		t.Fatal("la remise n’apparaît pas à l’article 2")
	}
	// Sans code attaché, le contrat reste général plutôt que d’annoncer un taux inventé.
	plain := BuildAmbassadorContract("")
	for _, a := range plain.Articles {
		for _, b := range a.Blocks {
			for _, bullet := range b.Bullets {
				if strings.Contains(bullet, "%") {
					t.Fatalf("taux inventé sans code : %q", bullet)
				}
			}
		}
	}
}
