package models

import "testing"

// Le prix remisé est ce qu’on encaisse : il doit tomber au centime, sans dérive flottante
// ni montant négatif quand la remise dépasse le prix de l’offre.
func TestPromoDiscountedPriceEUR(t *testing.T) {
	cases := []struct {
		name  string
		promo *PromoCode
		base  float64
		want  float64
	}{
		{"sans code", nil, 9.99, 9.99},
		{"30 %", &PromoCode{PercentOff: 30}, 9.99, 6.99},
		{"100 % — gratuit", &PromoCode{PercentOff: 100}, 9.99, 0},
		{"pourcentage à zéro", &PromoCode{PercentOff: 0}, 9.99, 9.99},
		// Codes créés avant l’ajout des remises en euros : kind vide = pourcentage.
		{"kind absent", &PromoCode{PercentOff: 50}, 10, 5},
		{"5 €", &PromoCode{Kind: PromoKindAmount, AmountOffCents: 500}, 9.99, 4.99},
		{"2,50 €", &PromoCode{Kind: PromoKindAmount, AmountOffCents: 250}, 9.99, 7.49},
		// Une remise plus grosse que le prix ne crée pas un avoir.
		{"remise supérieure au prix", &PromoCode{Kind: PromoKindAmount, AmountOffCents: 2000}, 9.99, 0},
		// Un code en euros ignore le pourcentage resté en base, et l’inverse.
		{"euros prime sur le pourcentage", &PromoCode{Kind: PromoKindAmount, AmountOffCents: 100, PercentOff: 90}, 10, 9},
		{"pourcentage ignore le montant", &PromoCode{Kind: PromoKindPercent, AmountOffCents: 900, PercentOff: 10}, 10, 9},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.promo.DiscountedPriceEUR(tc.base); got != tc.want {
				t.Fatalf("prix remisé = %v, attendu %v", got, tc.want)
			}
		})
	}
}

func TestPromoDiscountEUR(t *testing.T) {
	p := &PromoCode{PercentOff: 30}
	if got := p.DiscountEUR(9.99); got != 3 {
		t.Fatalf("remise = %v, attendu 3", got)
	}
	a := &PromoCode{Kind: PromoKindAmount, AmountOffCents: 2000}
	if got := a.DiscountEUR(9.99); got != 9.99 {
		t.Fatalf("remise plafonnée au prix = %v, attendu 9.99", got)
	}
}

// Une remise doit retirer quelque chose : un code à 0 % ou 0 € ne sert à rien et
// passerait inaperçu jusqu’au paiement.
func TestPromoValidateDiscount(t *testing.T) {
	cases := []struct {
		name  string
		promo PromoCode
		valid bool
	}{
		{"30 %", PromoCode{PercentOff: 30}, true},
		{"0 %", PromoCode{PercentOff: 0}, false},
		{"101 %", PromoCode{PercentOff: 101}, false},
		{"5 €", PromoCode{Kind: PromoKindAmount, AmountOffCents: 500}, true},
		{"0 €", PromoCode{Kind: PromoKindAmount, AmountOffCents: 0}, false},
		{"montant négatif", PromoCode{Kind: PromoKindAmount, AmountOffCents: -500}, false},
		{"montant démesuré", PromoCode{Kind: PromoKindAmount, AmountOffCents: PromoAmountOffMaxCents + 1}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.promo.ValidateDiscount()
			if tc.valid && err != nil {
				t.Fatalf("refusé à tort : %v", err)
			}
			if !tc.valid && err == nil {
				t.Fatal("accepté à tort")
			}
		})
	}
}

// Le libellé part dans le contrat d’ambassadeur : il doit se lire comme une phrase.
func TestPromoDiscountLabel(t *testing.T) {
	cases := []struct {
		promo PromoCode
		want  string
	}{
		{PromoCode{PercentOff: 30}, "30 %"},
		{PromoCode{Kind: PromoKindAmount, AmountOffCents: 500}, "5 €"},
		{PromoCode{Kind: PromoKindAmount, AmountOffCents: 450}, "4,50 €"},
	}
	for _, tc := range cases {
		if got := tc.promo.DiscountLabel(); got != tc.want {
			t.Fatalf("libellé = %q, attendu %q", got, tc.want)
		}
	}
}

func TestPromoUsableAndExhausted(t *testing.T) {
	exhausted := &PromoCode{Active: true, PercentOff: 30, MaxUses: 10, Uses: 10}
	if !exhausted.Exhausted() {
		t.Fatal("quota atteint non détecté")
	}
	if exhausted.Usable() {
		t.Fatal("un code épuisé reste utilisable")
	}
	unlimited := &PromoCode{Active: true, PercentOff: 30, MaxUses: 0, Uses: 999}
	if unlimited.Exhausted() || !unlimited.Usable() {
		t.Fatal("max_uses = 0 doit valoir illimité")
	}
	if (&PromoCode{Active: false, PercentOff: 30}).Usable() {
		t.Fatal("un code désactivé reste utilisable")
	}
}
