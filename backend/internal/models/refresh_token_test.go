package models

import (
	"testing"
	"time"
)

func TestRefreshTokenValid(t *testing.T) {
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	past := now.Add(-time.Hour)

	cases := []struct {
		name  string
		token RefreshToken
		want  bool
	}{
		{
			name:  "neuf et non expiré",
			token: RefreshToken{ExpiresAt: now.Add(time.Hour)},
			want:  true,
		},
		{
			name:  "expiré",
			token: RefreshToken{ExpiresAt: past},
			want:  false,
		},
		{
			name:  "déjà échangé",
			token: RefreshToken{ExpiresAt: now.Add(time.Hour), UsedAt: &past},
			want:  false,
		},
		{
			name:  "révoqué",
			token: RefreshToken{ExpiresAt: now.Add(time.Hour), RevokedAt: &past},
			want:  false,
		},
		{
			name:  "expire pile maintenant",
			token: RefreshToken{ExpiresAt: now},
			want:  false,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.token.Valid(now); got != c.want {
				t.Fatalf("Valid() = %v, attendu %v", got, c.want)
			}
		})
	}
}
