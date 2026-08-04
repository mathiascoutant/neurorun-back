package store

import "errors"

var (
	ErrNotFound       = errors.New("not found")
	ErrDuplicateEmail = errors.New("email already registered")
	ErrDuplicatePromo = errors.New("promo code already exists")
	// ErrDuplicateLiveRun : une course avec le même client_run_id existe déjà
	// pour cet utilisateur. Ce n'est pas une erreur côté client : c'est un
	// réessai, et l'appelant doit renvoyer la course déjà enregistrée.
	ErrDuplicateLiveRun = errors.New("live run already recorded")
)
