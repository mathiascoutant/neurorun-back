package auth

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestNewRefreshTokenIsUnpredictableAndURLSafe(t *testing.T) {
	seen := make(map[string]bool, 256)
	for i := 0; i < 256; i++ {
		tok, err := NewRefreshToken()
		if err != nil {
			t.Fatalf("NewRefreshToken: %v", err)
		}
		if seen[tok] {
			t.Fatalf("jeton répété au tirage %d : l'aléa est cassé", i)
		}
		seen[tok] = true

		raw, err := base64.RawURLEncoding.DecodeString(tok)
		if err != nil {
			t.Fatalf("jeton non décodable en base64url : %q", tok)
		}
		if len(raw) != refreshTokenBytes {
			t.Fatalf("entropie = %d octets, attendu %d", len(raw), refreshTokenBytes)
		}
		// Le jeton voyage en JSON et en en-tête : pas de caractère à échapper.
		if strings.ContainsAny(tok, "+/=") {
			t.Fatalf("jeton non URL-safe : %q", tok)
		}
	}
}

func TestHashRefreshTokenIsStableAndDistinct(t *testing.T) {
	const a = "jeton-a"
	const b = "jeton-b"

	if HashRefreshToken(a) != HashRefreshToken(a) {
		t.Fatal("le même jeton doit donner la même empreinte, sinon rien ne se retrouve en base")
	}
	if HashRefreshToken(a) == HashRefreshToken(b) {
		t.Fatal("deux jetons différents partagent une empreinte")
	}
	if got := len(HashRefreshToken(a)); got != 64 {
		t.Fatalf("empreinte de %d caractères, attendu 64 (SHA-256 en hexadécimal)", got)
	}
	// L'empreinte ne doit jamais laisser transparaître le jeton lui-même.
	if strings.Contains(HashRefreshToken(a), a) {
		t.Fatal("l'empreinte contient le jeton en clair")
	}
}
