package handlers

import (
	"strings"
	"testing"
	"time"

	"runapp/internal/models"
	"runapp/internal/shoes"
)

func TestShoesCoachBlockFlagsWornPair(t *testing.T) {
	now := time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC)
	retired := now.AddDate(0, -2, 0)
	views := []shoeView{
		{Shoe: models.Shoe{Brand: "Nike", Model: "Pegasus 41", Category: shoes.CategoryDaily},
			Runs: 60, Wear: shoes.ComputeWear(760, 700, now.AddDate(-1, 0, 0), now)},
		{Shoe: models.Shoe{Brand: "Hoka", Model: "Speedgoat 6", Nickname: "les vertes", Category: shoes.CategoryTrail},
			Runs: 4, Wear: shoes.ComputeWear(80, 650, now.AddDate(0, -1, 0), now)},
		{Shoe: models.Shoe{Brand: "Asics", Model: "Novablast", RetiredAt: &retired}, Retired: true},
	}
	block := shoesCoachBlock(views, time.UTC)

	for _, want := range []string{
		"Nike Pegasus 41 (Polyvalente) : 760 km sur 700 km",
		"USÉE",
		"Hoka Speedgoat 6 « les vertes » (Trail)",
		"quasi neuve",
		"1 paire(s) mise(s) au placard",
	} {
		if !strings.Contains(block, want) {
			t.Fatalf("%q absent du bloc :\n%s", want, block)
		}
	}
	if strings.Contains(block, "Novablast") {
		t.Fatalf("une paire au placard ne doit pas être proposée :\n%s", block)
	}
}

func TestShoesCoachBlockWithoutPairs(t *testing.T) {
	if block := shoesCoachBlock(nil, time.UTC); !strings.Contains(block, "Aucune paire suivie") {
		t.Fatalf("bloc inattendu sans paire :\n%s", block)
	}
}
