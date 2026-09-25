package sandbox_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/cloud/internal/sandbox"
	"github.com/aoagents/agent-orchestrator/cloud/internal/sandbox/coder"
	"github.com/aoagents/agent-orchestrator/cloud/internal/sandbox/createos"
	"github.com/aoagents/agent-orchestrator/cloud/internal/sandbox/docker"
)

type createTransport func(*http.Request) (*http.Response, error)

func (fn createTransport) RoundTrip(r *http.Request) (*http.Response, error) { return fn(r) }

func TestProvidersClassifyOnlyDefinitiveCreateRejections(t *testing.T) {
	for _, providerName := range []string{"docker", "coder", "createos"} {
		for _, status := range []int{400, 401, 403, 404, 405, 410, 422, 429, 408, 409, 500, 503, 200, 0} {
			t.Run(providerName+"/"+strconv.Itoa(status), func(t *testing.T) {
				client := &http.Client{Transport: createTransport(func(r *http.Request) (*http.Response, error) {
					code, body := status, `{}`
					switch {
					case strings.HasPrefix(r.URL.Path, "/v1.45/volumes/"):
						if r.Method == http.MethodGet {
							code = 404
						} else {
							code = 201
							data, err := io.ReadAll(r.Body)
							if err != nil {
								return nil, err
							}
							body = string(data)
						}
					default:
						if status == 0 {
							return nil, context.DeadlineExceeded
						}
						if status == 200 {
							body = "invalid success response"
						}
					}
					return &http.Response{StatusCode: code, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
				})}
				var provider sandbox.Provider
				var err error
				switch providerName {
				case "docker":
					provider, err = docker.New(docker.Config{Host: "http://provider.test", HTTPClient: client, WorkerImage: "missing:tag", Namespace: "review", APIVersion: "1.45"})
				case "coder":
					provider, err = coder.New(coder.Config{BaseURL: "http://provider.test", HTTPClient: client, Owner: "worker", Token: "test", TemplateID: "11111111-1111-1111-1111-111111111111"})
				case "createos":
					provider = createos.New(createos.Config{BaseURL: "http://provider.test", HTTPClient: client, DefaultShape: "small"})
				}
				if err != nil {
					t.Fatal(err)
				}
				_, err = provider.Create(context.Background(), sandbox.Spec{SessionID: "11111111-1111-1111-1111-111111111111", OrgID: "22222222-2222-2222-2222-222222222222"})
				wantRejected := status >= 400 && status < 500 && status != 408 && status != 409
				if err == nil || errors.Is(err, sandbox.ErrCreateRejected) != wantRejected {
					t.Fatalf("status=%d err=%v rejected=%v", status, err, wantRejected)
				}
				if status == 404 && !errors.Is(err, sandbox.ErrNotFound) {
					t.Fatal("underlying provider error was lost")
				}
			})
		}
	}
}
