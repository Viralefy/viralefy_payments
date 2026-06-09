package domain

import "errors"

// Erros canônicos do serviço de pagamentos. Espelham os do monólito pra
// que a camada de aplicação possa ser copy-paste-friendly.
var (
	ErrNotFound     = errors.New("not found")
	ErrConflict     = errors.New("conflict")
	ErrInvalidInput = errors.New("invalid input")
	ErrUnauthorized = errors.New("unauthorized")
)
