package verify

import "errors"

var (
	ErrRateLimited     = errors.New("rate limit exceeded, slow down and retry")
	ErrQueueFull       = errors.New("verification queue is full, retry later")
	ErrUnknownContract = errors.New("contract not found on the configured network")
	ErrSACUnsupported  = errors.New("stellar asset contracts have no WASM to verify")
	ErrNotFound        = errors.New("not found")
	ErrInvalidRequest  = errors.New("invalid request")
)
