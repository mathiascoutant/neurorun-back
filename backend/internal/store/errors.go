package store

import "errors"

var (
	ErrNotFound       = errors.New("not found")
	ErrDuplicateEmail = errors.New("email already registered")
	ErrDuplicatePromo = errors.New("promo code already exists")
)
