package store

import "errors"

var (
	ErrNotFound       = errors.New("not found")
	ErrDuplicateEmail = errors.New("email already registered")
	ErrDuplicatePromo = errors.New("promo code already exists")
	// ErrDuplicateAmbassador : ce compte a déjà un contrat d’ambassadeur.
	ErrDuplicateAmbassador = errors.New("ambassador already exists")
	// ErrDuplicateLiveRun : une course avec le même client_run_id existe déjà
	// pour cet utilisateur. Ce n'est pas une erreur côté client : c'est un
	// réessai, et l'appelant doit renvoyer la course déjà enregistrée.
	ErrDuplicateLiveRun = errors.New("live run already recorded")
	// ErrDuplicateVmaTest : même logique pour un test de VMA renvoyé deux fois.
	ErrDuplicateVmaTest = errors.New("vma test already recorded")
)
