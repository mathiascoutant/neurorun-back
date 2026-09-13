package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

var ErrInvalidCredentials = errors.New("invalid credentials")

// refreshTokenBytes : 32 octets d’aléa, soit 256 bits — hors de portée d’une
// attaque par force brute, et assez court pour tenir dans un en-tête.
const refreshTokenBytes = 32

// NewRefreshToken renvoie un jeton opaque à remettre au client. Opaque et non
// signé : contrairement à un JWT, il ne vaut que par sa présence en base, donc
// une déconnexion le coupe pour de bon.
func NewRefreshToken() (string, error) {
	b := make([]byte, refreshTokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// HashRefreshToken : empreinte stockée en base. SHA-256 nu suffit ici — le
// jeton est un aléa de 256 bits, pas un mot de passe : il n’y a rien à
// deviner, donc rien à ralentir avec bcrypt.
func HashRefreshToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func HashPassword(password string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	return string(b), err
}

func CheckPassword(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

type Claims struct {
	UserID string `json:"sub"`
	jwt.RegisteredClaims
}

func SignJWT(userIDHex, secret string, ttl time.Duration) (string, error) {
	claims := Claims{
		UserID: userIDHex,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(ttl)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}
	t := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return t.SignedString([]byte(secret))
}

func ParseJWT(tokenStr, secret string) (*Claims, error) {
	token, err := jwt.ParseWithClaims(tokenStr, &Claims{}, func(t *jwt.Token) (any, error) {
		return []byte(secret), nil
	})
	if err != nil {
		return nil, err
	}
	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return nil, errors.New("invalid token")
	}
	return claims, nil
}

type StravaStateClaims struct {
	UserID string `json:"uid"`
	jwt.RegisteredClaims
}

func SignStravaState(userIDHex, secret string, ttl time.Duration) (string, error) {
	claims := StravaStateClaims{
		UserID: userIDHex,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(ttl)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			Subject:   "strava_oauth",
		},
	}
	t := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return t.SignedString([]byte(secret))
}

func ParseStravaState(state, secret string) (userIDHex string, err error) {
	token, err := jwt.ParseWithClaims(state, &StravaStateClaims{}, func(t *jwt.Token) (any, error) {
		return []byte(secret), nil
	})
	if err != nil {
		return "", err
	}
	claims, ok := token.Claims.(*StravaStateClaims)
	if !ok || !token.Valid || claims.Subject != "strava_oauth" {
		return "", errors.New("invalid strava state")
	}
	return claims.UserID, nil
}
