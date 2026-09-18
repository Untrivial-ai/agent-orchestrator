package controllers_test

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/controllers"
)

type dummyPayload struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

func TestDecodeRequestJSON_Table(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		policy     controllers.DecodePolicy
		wantErr    error
		wantMaxErr bool
		checkOut   func(t *testing.T, out dummyPayload)
	}{
		{
			name:   "valid single JSON object without trailing data",
			body:   `{"name":"test","count":42}`,
			policy: controllers.DecodePolicy{MaxBytes: 1024, DisallowUnknownFields: true, AllowEmpty: false},
			checkOut: func(t *testing.T, out dummyPayload) {
				if out.Name != "test" || out.Count != 42 {
					t.Fatalf("unexpected out: %+v", out)
				}
			},
		},
		{
			name:   "valid single JSON object with trailing whitespace",
			body:   "{\"name\":\"test\",\"count\":42}  \r\n\t  \n",
			policy: controllers.DecodePolicy{MaxBytes: 1024, DisallowUnknownFields: true, AllowEmpty: false},
			checkOut: func(t *testing.T, out dummyPayload) {
				if out.Name != "test" || out.Count != 42 {
					t.Fatalf("unexpected out: %+v", out)
				}
			},
		},
		{
			name:    "concatenated second JSON document rejected",
			body:    `{"name":"test","count":42} {"extra":"value"}`,
			policy:  controllers.DecodePolicy{MaxBytes: 1024, DisallowUnknownFields: false, AllowEmpty: false},
			wantErr: controllers.ErrMultipleJSONDocuments,
		},
		{
			name:    "concatenated primitive value rejected",
			body:    `{"name":"test","count":42} 123`,
			policy:  controllers.DecodePolicy{MaxBytes: 1024, DisallowUnknownFields: false, AllowEmpty: false},
			wantErr: controllers.ErrMultipleJSONDocuments,
		},
		{
			name:    "trailing non-JSON junk rejected",
			body:    `{"name":"test","count":42} trailing garbage`,
			policy:  controllers.DecodePolicy{MaxBytes: 1024, DisallowUnknownFields: false, AllowEmpty: false},
			wantErr: controllers.ErrTrailingData,
		},
		{
			name:    "empty body rejected when AllowEmpty is false",
			body:    "",
			policy:  controllers.DecodePolicy{MaxBytes: 1024, DisallowUnknownFields: false, AllowEmpty: false},
			wantErr: controllers.ErrEmptyBody,
		},
		{
			name:   "empty body accepted when AllowEmpty is true",
			body:   "",
			policy: controllers.DecodePolicy{MaxBytes: 1024, DisallowUnknownFields: false, AllowEmpty: true},
			checkOut: func(t *testing.T, out dummyPayload) {
				if out.Name != "" || out.Count != 0 {
					t.Fatalf("out should be zero value, got: %+v", out)
				}
			},
		},
		{
			name:    "whitespace-only body rejected when AllowEmpty is false",
			body:    "   \r\n\t   ",
			policy:  controllers.DecodePolicy{MaxBytes: 1024, DisallowUnknownFields: false, AllowEmpty: false},
			wantErr: controllers.ErrEmptyBody,
		},
		{
			name:   "whitespace-only body accepted when AllowEmpty is true",
			body:   "   \r\n\t   ",
			policy: controllers.DecodePolicy{MaxBytes: 1024, DisallowUnknownFields: false, AllowEmpty: true},
			checkOut: func(t *testing.T, out dummyPayload) {
				if out.Name != "" || out.Count != 0 {
					t.Fatalf("out should be zero value, got: %+v", out)
				}
			},
		},
		{
			name:    "unknown fields rejected when DisallowUnknownFields is true",
			body:    `{"name":"test","count":42,"unknownField":"bad"}`,
			policy:  controllers.DecodePolicy{MaxBytes: 1024, DisallowUnknownFields: true, AllowEmpty: false},
			wantErr: errors.New(`json: unknown field "unknownField"`),
		},
		{
			name:   "unknown fields accepted when DisallowUnknownFields is false",
			body:   `{"name":"test","count":42,"unknownField":"ok"}`,
			policy: controllers.DecodePolicy{MaxBytes: 1024, DisallowUnknownFields: false, AllowEmpty: false},
			checkOut: func(t *testing.T, out dummyPayload) {
				if out.Name != "test" || out.Count != 42 {
					t.Fatalf("unexpected out: %+v", out)
				}
			},
		},
		{
			name:       "payload exceeding MaxBytes rejected",
			body:       `{"name":"` + strings.Repeat("A", 200) + `","count":1}`,
			policy:     controllers.DecodePolicy{MaxBytes: 100, DisallowUnknownFields: false, AllowEmpty: false},
			wantMaxErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/test", strings.NewReader(tt.body))

			var out dummyPayload
			err := controllers.DecodeRequestJSON(rec, req, &out, tt.policy)

			if tt.wantMaxErr {
				var maxBytesErr *http.MaxBytesError
				if !errors.As(err, &maxBytesErr) {
					t.Fatalf("expected MaxBytesError, got: %v", err)
				}
				return
			}

			if tt.wantErr != nil {
				if err == nil {
					t.Fatalf("expected error %v, got nil", tt.wantErr)
				}
				if !errors.Is(err, tt.wantErr) && !strings.Contains(err.Error(), tt.wantErr.Error()) {
					t.Fatalf("got error %v, want matching %v", err, tt.wantErr)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.checkOut != nil {
				tt.checkOut(t, out)
			}
		})
	}
}

func TestDecodeRequestJSON_NilBody(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/test", nil)
	req.Body = nil

	var out dummyPayload
	// AllowEmpty: false
	err := controllers.DecodeRequestJSON(nil, req, &out, controllers.DecodePolicy{AllowEmpty: false})
	if !errors.Is(err, controllers.ErrEmptyBody) {
		t.Fatalf("expected ErrEmptyBody for nil body, got: %v", err)
	}

	// AllowEmpty: true
	err = controllers.DecodeRequestJSON(nil, req, &out, controllers.DecodePolicy{AllowEmpty: true})
	if err != nil {
		t.Fatalf("expected nil error for nil body with AllowEmpty, got: %v", err)
	}
}

type byteStreamReader struct {
	b byte
}

func (r byteStreamReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = r.b
	}
	return len(p), nil
}

func TestDecodeRequestJSON_PaddedPrefixOverflowDetected(t *testing.T) {
	// A valid JSON prefix padded with whitespace up to the limit, followed by extra data
	// beyond the limit, must be detected as an overflow error and NOT mistaken for EOF.
	capLimit := int64(100)
	prefix := `{"name":"abc"}`
	paddingLen := capLimit - int64(len(prefix))

	stream := io.MultiReader(
		strings.NewReader(prefix),
		io.LimitReader(byteStreamReader{b: ' '}, paddingLen),
		strings.NewReader("EXTRA_TRUNCATED_BYTES"),
	)

	req := httptest.NewRequest(http.MethodPost, "/test", stream)
	rec := httptest.NewRecorder()

	var out dummyPayload
	err := controllers.DecodeRequestJSON(rec, req, &out, controllers.DecodePolicy{
		MaxBytes: capLimit,
	})

	var maxBytesErr *http.MaxBytesError
	if !errors.As(err, &maxBytesErr) {
		t.Fatalf("expected MaxBytesError when valid prefix is padded to limit with trailing overflow, got: %v", err)
	}
}

func TestDecodeRequestJSON_PaddedPrefixWithinCapAccepted(t *testing.T) {
	capLimit := int64(100)
	prefix := `{"name":"abc"}`
	paddingLen := capLimit - int64(len(prefix))

	stream := io.MultiReader(
		strings.NewReader(prefix),
		io.LimitReader(byteStreamReader{b: ' '}, paddingLen),
	)

	req := httptest.NewRequest(http.MethodPost, "/test", stream)
	rec := httptest.NewRecorder()

	var out dummyPayload
	err := controllers.DecodeRequestJSON(rec, req, &out, controllers.DecodePolicy{
		MaxBytes: capLimit,
	})
	if err != nil {
		t.Fatalf("expected success for valid payload padded with spaces within cap, got: %v", err)
	}
	if out.Name != "abc" {
		t.Fatalf("expected Name=abc, got: %q", out.Name)
	}
}

func TestDecodeConvenienceHelpers(t *testing.T) {
	// DecodeJSONStrictBounded
	req := httptest.NewRequest(http.MethodPost, "/test", strings.NewReader(`{"name":"strict","count":1}`))
	var out dummyPayload
	if err := controllers.DecodeJSONStrictBounded(nil, req, &out, 1024); err != nil {
		t.Fatalf("DecodeJSONStrictBounded failed: %v", err)
	}
	if out.Name != "strict" {
		t.Fatalf("expected strict, got %q", out.Name)
	}

	// DecodeJSONBounded with unknown field
	req = httptest.NewRequest(http.MethodPost, "/test", strings.NewReader(`{"name":"lenient","unknown":true}`))
	out = dummyPayload{}
	if err := controllers.DecodeJSONBounded(nil, req, &out, 1024); err != nil {
		t.Fatalf("DecodeJSONBounded failed: %v", err)
	}
	if out.Name != "lenient" {
		t.Fatalf("expected lenient, got %q", out.Name)
	}

	// DecodeJSONStrict rejects unknown field
	req = httptest.NewRequest(http.MethodPost, "/test", strings.NewReader(`{"name":"strict","unknown":true}`))
	if err := controllers.DecodeJSONStrict(req, &out); err == nil {
		t.Fatalf("DecodeJSONStrict should reject unknown fields")
	}

	// DecodeJSON rejects trailing junk
	req = httptest.NewRequest(http.MethodPost, "/test", strings.NewReader(`{"name":"lenient"} junk`))
	if err := controllers.DecodeJSON(req, &out); err == nil {
		t.Fatalf("DecodeJSON should reject trailing junk")
	}
}
