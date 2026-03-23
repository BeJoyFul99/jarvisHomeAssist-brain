package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"jarvishomeassist-brain/internal/logger"
)

// LogsHandler serves server log data.
type LogsHandler struct {
	Logger *logger.Logger
}

// List handles GET /api/v1/admin/logs?page=1&per_page=100&level=info&component=db&search=keyword
func (h *LogsHandler) List(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	perPage, _ := strconv.Atoi(c.DefaultQuery("per_page", "100"))
	levelFilter := strings.ToLower(c.Query("level"))
	componentFilter := strings.ToLower(c.Query("component"))
	searchFilter := strings.ToLower(c.Query("search"))

	result, err := h.Logger.ReadPage(page, perPage)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to read logs"})
		return
	}

	// Apply filters if any
	if levelFilter != "" || componentFilter != "" || searchFilter != "" {
		filtered := make([]logger.Entry, 0, len(result.Entries))
		for _, e := range result.Entries {
			if levelFilter != "" && strings.ToLower(e.Level) != levelFilter {
				continue
			}
			if componentFilter != "" && !strings.Contains(strings.ToLower(e.Component), componentFilter) {
				continue
			}
			if searchFilter != "" && !strings.Contains(strings.ToLower(e.Message), searchFilter) {
				continue
			}
			filtered = append(filtered, e)
		}
		result.Entries = filtered
	}

	c.JSON(http.StatusOK, result)
}

// Stream handles GET /api/v1/admin/logs/stream — SSE stream of new log entries.
func (h *LogsHandler) Stream(c *gin.Context) {
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("X-Accel-Buffering", "no")

	ch, unsubscribe := h.Logger.Subscribe()
	defer unsubscribe()

	// Send initial connected event
	fmt.Fprintf(c.Writer, "data: {\"type\":\"connected\"}\n\n")
	c.Writer.Flush()

	clientGone := c.Request.Context().Done()

	for {
		select {
		case <-clientGone:
			return
		case entry, ok := <-ch:
			if !ok {
				return
			}
			data, _ := json.Marshal(entry)
			fmt.Fprintf(c.Writer, "data: %s\n\n", data)
			c.Writer.Flush()
		}
	}
}
