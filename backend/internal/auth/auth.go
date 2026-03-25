package auth

import (
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

var ErrInvalidCredentials = errors.New("invalid credentials")

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
