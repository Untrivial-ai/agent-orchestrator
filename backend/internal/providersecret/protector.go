package providersecret

// Protector keeps the service independent from the platform encryption API.
type Protector interface {
	Protect([]byte) ([]byte, error)
	Unprotect([]byte) ([]byte, error)
}
