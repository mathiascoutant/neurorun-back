package scene3d

import "testing"

const validBase = `"background":{"top":"#000000","bottom":"zzz"},"floor":"shadow","lighting":"studio","camera":{"distance":4,"height":1,"fov":30}`

func TestParseSceneDropsBrokenPartsAndDecals(t *testing.T) {
	raw := `{"summary":" ok ",` + validBase + `,
	"parts":[
		{"name":"buste","shape":"loft","dimensions":[],"points":[],"sections":[[0,0,0,1,0.5],[0,2,0,1,0.5]],"capped":false,"position":[0,0,0],"rotation":[0,0,0],"material":{"color":"nope","roughness":3,"metalness":-1,"opacity":0,"finish":"laine"}},
		{"name":"sans section","shape":"loft","dimensions":[],"points":[],"sections":[[0,0,0]],"capped":false,"position":[0,0,0],"rotation":[0,0,0],"material":{"color":"#000000","roughness":1,"metalness":0,"opacity":1,"finish":"plain"}},
		{"name":"forme inconnue","shape":"script","dimensions":[1,1,1],"points":[],"sections":[],"capped":false,"position":[0,0,0],"rotation":[0,0,0],"material":{"color":"#000000","roughness":1,"metalness":0,"opacity":1,"finish":"plain"}}
	],
	"decals":[
		{"photo_id":"aabbccdd","crop":[0.5,0.5,0.9,0.2],"part":"buste","position":[0,1,0.5],"normal":[0,0,1],"width":0.6,"rotation":0,"remove_background":true},
		{"photo_id":"inconnue","crop":[0,0,1,1],"part":"buste","position":[0,1,0.5],"normal":[0,0,1],"width":0.6,"rotation":0,"remove_background":true},
		{"photo_id":"aabbccdd","crop":[0,0,1,1],"part":"absente","position":[0,1,0.5],"normal":[0,0,1],"width":0.6,"rotation":0,"remove_background":true},
		{"photo_id":"aabbccdd","crop":[0,0,1,1],"part":"buste","position":[0,1,0.5],"normal":[0,0,0],"width":0.6,"rotation":0,"remove_background":true}
	]}`
	s, err := ParseScene(raw, []string{"aabbccdd00112233.jpg"})
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Parts) != 1 || s.Parts[0].Name != "buste" {
		t.Fatalf("pièces gardées : %+v", s.Parts)
	}
	m := s.Parts[0].Material
	if m.Color != "#808080" || m.Roughness != 1 || m.Metalness != 0 || m.Opacity != 0.05 || m.Finish != "plain" {
		t.Fatalf("matière non corrigée : %+v", m)
	}
	if s.Background.Bottom != "#000000" {
		t.Fatalf("fond non corrigé : %+v", s.Background)
	}
	if len(s.Decals) != 1 {
		t.Fatalf("impressions gardées : %+v", s.Decals)
	}
	if got := s.Decals[0].Crop[2]; got != 0.5 {
		t.Fatalf("recadrage hors de la photo : %v", got)
	}
}

func TestParseSceneNeedsAPart(t *testing.T) {
	if _, err := ParseScene(`{"summary":"",`+validBase+`,"parts":[],"decals":[]}`, nil); err == nil {
		t.Fatal("une scène vide doit être refusée")
	}
	if _, err := ParseScene(`pas du json`, nil); err == nil {
		t.Fatal("une réponse illisible doit être refusée")
	}
}
