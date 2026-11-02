package handlers

import (
	"encoding/base64"
	"testing"
)

func TestDecodeShopModel3DRenders(t *testing.T) {
	jpeg := base64.StdEncoding.EncodeToString([]byte{0xFF, 0xD8, 0xFF, 0xE0, 0, 0x10, 'J', 'F', 'I', 'F'})
	png := base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\n"))

	if got, ok := decodeShopModel3DRenders([]string{jpeg, jpeg}); !ok || len(got) != 2 {
		t.Fatalf("deux JPEG doivent passer : %v, %v", got, ok)
	}
	for name, input := range map[string][]string{
		"pas du base64": {"%%%"},
		"pas un JPEG":   {png},
		"trop de vues":  {jpeg, jpeg, jpeg, jpeg, jpeg},
	} {
		if _, ok := decodeShopModel3DRenders(input); ok {
			t.Errorf("%s : doit être refusé", name)
		}
	}
}

func TestShopModel3DScenePhotos(t *testing.T) {
	raw := `{"decals":[{"photo_id":"aabbccdd"},{"photo_id":"aabbccdd"},{"photo_id":"disparue"}]}`
	got, err := shopModel3DScenePhotos(raw, []string{"aabbccdd0011.jpg", "99887766aabb.jpg"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "aabbccdd0011.jpg" {
		t.Fatalf("seule la photo reprise doit être publique : %v", got)
	}
	if got, _ := shopModel3DScenePhotos(`{"decals":[]}`, []string{"aabbccdd0011.jpg"}); len(got) != 0 {
		t.Fatalf("sans impression, aucune photo publique : %v", got)
	}
}
