package handlers

import (
	"fmt"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"

	"jarvishomeassist-brain/internal/sse"
)

// SSEHandler serves the Server-Sent Events stream.
type SSEHandler struct {
	Hub *sse.Hub
}

// Stream handles GET /api/v1/events — keeps the connection open and pushes events.
func (h *SSEHandler) Stream(c *gin.Context) {
	// Identify the connected user from JWT middleware context
	email, _ := c.Get("user_email")
	emailStr, _ := email.(string)

	// Set SSE headers
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("X-Accel-Buffering", "no") // for nginx proxies

	// Subscribe to the hub
	ch, unsubscribe := h.Hub.Subscribe(emailStr)
	defer unsubscribe()

	// Send initial connection event
	fmt.Fprintf(c.Writer, "data: {\"type\":\"connected\",\"data\":{}}\n\n")
	c.Writer.Flush()

	// Detect client disconnect
	clientGone := c.Request.Context().Done()

	for {
		select {
		case <-clientGone:
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			_, err := c.Writer.Write(msg)
			if err != nil {
				return
			}
			c.Writer.Flush()
		}
	}
}

// HealthStream handles GET /api/v1/events/health — returns SSE hub stats.
func (h *SSEHandler) HealthStream(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"connected_clients": h.Hub.Count(),
	})
}

// Ensure gin.ResponseWriter implements io.Writer (it does).
var _ io.Writer = (gin.ResponseWriter)(nil)
