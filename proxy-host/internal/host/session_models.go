package host

import (
	"bytes"
	"encoding/json"
	"net/http"
	"slices"

	"github.com/gin-gonic/gin"
	proxycore "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy"
)

// serveSessionModels lets the SDK write its model list, then removes every model the session's account cannot run.
func serveSessionModels(c *gin.Context, authID string) {
	allowed := proxycore.GlobalModelRegistry().GetModelsForClient(authID)
	held := &heldAnswer{ResponseWriter: c.Writer}
	c.Writer = held
	c.Next()
	c.Writer = held.ResponseWriter
	body := held.body.Bytes()
	if c.Writer.Status() == http.StatusOK {
		body = onlyModels(body, allowed)
	}
	c.Writer.Header().Del("Content-Length")
	_, _ = c.Writer.Write(body)
}

// onlyModels keeps the listed entries whose id is allowed; a body that is not a model list is returned as it came.
func onlyModels(body []byte, allowed []*proxycore.ModelInfo) []byte {
	var list map[string]json.RawMessage
	var entries []json.RawMessage
	if json.Unmarshal(body, &list) != nil || list["data"] == nil || json.Unmarshal(list["data"], &entries) != nil {
		return body
	}
	kept := []json.RawMessage{}
	for _, entry := range entries {
		var model struct{ ID string }
		if json.Unmarshal(entry, &model) == nil && slices.ContainsFunc(allowed, func(m *proxycore.ModelInfo) bool { return m.ID == model.ID }) {
			kept = append(kept, entry)
		}
	}
	list["data"], _ = json.Marshal(kept)
	filtered, _ := json.Marshal(list)
	return filtered
}

type heldAnswer struct {
	gin.ResponseWriter
	body bytes.Buffer
}

func (w *heldAnswer) Write(data []byte) (int, error)    { return w.body.Write(data) }
func (w *heldAnswer) WriteString(s string) (int, error) { return w.body.WriteString(s) }
