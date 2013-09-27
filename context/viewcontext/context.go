// Package viewcontext provides context related HTTP Handlers for Rell.
package viewcontext

import (
	"net/http"

	"github.com/daaku/go.httpdev"

	"github.com/daaku/rell/context"
	"github.com/daaku/rell/view"
)

var version string

type Handler struct {
	ContextParser *context.Parser
}

// Handler for /info/ to see a JSON view of some server context.
func (h *Handler) Info(w http.ResponseWriter, r *http.Request) {
	context, err := h.ContextParser.FromRequest(r)
	if err != nil {
		view.Error(w, r, err)
		return
	}
	info := map[string]interface{}{
		"context":    context,
		"pageTabURL": context.PageTabURL("/"),
		"canvasURL":  context.CanvasURL("/"),
		"sdkURL":     context.SdkURL(),
		"version":    version,
	}
	httpdev.Info(info, w, r)
}
