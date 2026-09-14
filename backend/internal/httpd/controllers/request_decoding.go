package controllers

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

const (
	// DefaultMaxBodyBytes bounds ordinary JSON request bodies where no special
	// payload size (like attachments) is expected. 1 MiB is far above normal
	// JSON configuration/input sizes while guarding against unbounded memory allocation.
	DefaultMaxBodyBytes int64 = 1 << 20 // 1 MiB

	defaultMaxBodyBytes = DefaultMaxBodyBytes
)

var (
	// ErrEmptyBody signals an empty or whitespace-only request body when a body is required.
	ErrEmptyBody = fmt.Errorf("%w: request body is empty", io.EOF)
	// ErrTrailingData signals unexpected non-whitespace data after the first JSON document.
	ErrTrailingData = errors.New("unexpected trailing data after JSON document")
	// ErrMultipleJSONDocuments signals that more than one JSON document was present in the request body.
	ErrMultipleJSONDocuments = errors.New("multiple JSON documents in request body")
)

// DecodePolicy controls request decoding behavior.
type DecodePolicy struct {
	// MaxBytes limits the number of bytes read from r.Body. If <= 0, no MaxBytesReader wrapper is applied.
	MaxBytes int64
	// DisallowUnknownFields rejects keys not defined on the target struct.
	DisallowUnknownFields bool
	// AllowEmpty treats an empty or whitespace-only body as success without error.
	AllowEmpty bool
}

type decodePolicy = DecodePolicy

// DecodeRequestJSON decodes a single JSON value from r.Body into out according to policy.
// It bounds r.Body using http.MaxBytesReader when policy.MaxBytes > 0, verifies that exactly
// one JSON document is present (checking EOF to reject concatenated documents and trailing junk),
// and respects unknown-field and empty-body policies.
func DecodeRequestJSON(w http.ResponseWriter, r *http.Request, out any, policy DecodePolicy) error {
	if r.Body == nil {
		if policy.AllowEmpty {
			return nil
		}
		return ErrEmptyBody
	}

	if policy.MaxBytes > 0 {
		r.Body = http.MaxBytesReader(w, r.Body, policy.MaxBytes)
	}

	dec := json.NewDecoder(r.Body)
	if policy.DisallowUnknownFields {
		dec.DisallowUnknownFields()
	}

	if err := dec.Decode(out); err != nil {
		if errors.Is(err, io.EOF) {
			if policy.AllowEmpty {
				return nil
			}
			return ErrEmptyBody
		}
		return err
	}

	// Ensure there are no additional JSON tokens or trailing data.
	// dec.Decode skips whitespace looking for the next token. If only whitespace
	// remains until EOF, dec.Decode returns io.EOF.
	var extra json.RawMessage
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return ErrMultipleJSONDocuments
		}
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			return err
		}
		return ErrTrailingData
	}

	return nil
}

// decodeRequestJSON is a package-internal alias for DecodeRequestJSON.
func decodeRequestJSON(w http.ResponseWriter, r *http.Request, out any, policy decodePolicy) error {
	return DecodeRequestJSON(w, r, out, policy)
}

// DecodeJSONStrictBounded decodes a single JSON document with strict unknown-field
// rejection and an explicit byte cap. Empty bodies are rejected.
func DecodeJSONStrictBounded(w http.ResponseWriter, r *http.Request, out any, maxBytes int64) error {
	return DecodeRequestJSON(w, r, out, DecodePolicy{
		MaxBytes:              maxBytes,
		DisallowUnknownFields: true,
		AllowEmpty:            false,
	})
}

// decodeJSONStrictBounded is a package-internal helper that decodes a single JSON
// document with strict unknown-field rejection bounded by defaultMaxBodyBytes.
func decodeJSONStrictBounded(w http.ResponseWriter, r *http.Request, out any) error {
	return DecodeJSONStrictBounded(w, r, out, defaultMaxBodyBytes)
}

// DecodeJSONBounded decodes a single JSON document with lenient unknown-field
// handling and an explicit byte cap. Empty bodies are rejected.
func DecodeJSONBounded(w http.ResponseWriter, r *http.Request, out any, maxBytes int64) error {
	return DecodeRequestJSON(w, r, out, DecodePolicy{
		MaxBytes:              maxBytes,
		DisallowUnknownFields: false,
		AllowEmpty:            false,
	})
}

// DecodeJSONStrict rejects request bodies that include keys outside the target
// type. It is bounded by DefaultMaxBodyBytes and enforces single-document EOF.
func DecodeJSONStrict(r *http.Request, out any) error {
	return DecodeJSONStrictBounded(nil, r, out, DefaultMaxBodyBytes)
}

// decodeJSONStrict is a package-internal alias for DecodeJSONStrict, retaining
// compatibility with unmigrated callers.
func decodeJSONStrict(r *http.Request, out any) error {
	return DecodeJSONStrict(r, out)
}

// DecodeJSON decodes a single JSON document. It enforces single-document EOF.
// When r.Body has not already been wrapped by a caller-supplied MaxBytesReader,
// callers migrating to explicit policies should use DecodeJSONBounded.
func DecodeJSON(r *http.Request, out any) error {
	return DecodeRequestJSON(nil, r, out, DecodePolicy{
		MaxBytes:              0, // Preserves caller-applied MaxBytesReader (e.g. spawn, send, stage_attachments)
		DisallowUnknownFields: false,
		AllowEmpty:            false,
	})
}

// decodeJSON is a package-internal alias for DecodeJSON, retaining compatibility
// with unmigrated callers.
func decodeJSON(r *http.Request, out any) error {
	return DecodeJSON(r, out)
}
