package handlers

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/shirou/gopsutil/v3/host"
	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/mem"
	psnet "github.com/shirou/gopsutil/v4/net"

	"jarvishomeassist-brain/internal/logger"
	"jarvishomeassist-brain/internal/sse"
)

// StatusHandler serves system status as JSON or SSE stream.
type StatusHandler struct {
	Hub *sse.Hub
	Log *logger.Logger
}

// StartStatusTicker runs a background goroutine that pushes status updates
// through the SSE hub every 3 seconds.
func (h *StatusHandler) StartStatusTicker() {
	go func() {
		ticker := time.NewTicker(3 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			if h.Hub.Count() == 0 {
				continue // no clients, skip expensive collection
			}
			data, err := collectStatus()
			if err != nil {
				h.Log.Error("status", fmt.Sprintf("ticker collect error: %v", err))
				continue
			}
			h.Hub.Broadcast(sse.Event{
				Type: sse.EventStatusUpdate,
				Data: data,
			})
		}
	}()
}

// serviceName maps a well-known TCP port to a human label for the UI.
func serviceName(port uint32) string {
	switch port {
	case 22:
		return "SSH"
	case 80:
		return "HTTP"
	case 443:
		return "HTTPS"
	case 3000:
		return "Dev Server"
	case 5000:
		return "Jarvis API"
	case 5173:
		return "Vite"
	case 5432:
		return "PostgreSQL"
	case 6379:
		return "Redis"
	case 8080:
		return "HTTP Alt"
	case 3306:
		return "MySQL"
	case 27017:
		return "MongoDB"
	default:
		return "TCP"
	}
}

// getLocalIP returns the non-loopback local IP of the host.
func getLocalIP() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "Unknown"
	}
	for _, address := range addrs {
		if ipnet, ok := address.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
			if ipnet.IP.To4() != nil {
				return ipnet.IP.String()
			}
		}
	}
	return "Unknown"
}

// collectStatus gathers system metrics and returns them as a gin.H map.
func collectStatus() (gin.H, error) {
	// 1. CPU usage (single sample over 500ms — per-core; derive overall from average)
	perCorePercentage, err := cpu.Percent(500*time.Millisecond, true)
	if err != nil {
		return nil, fmt.Errorf("cpu per-core: %w", err)
	}

	cpuUsage := make([]gin.H, len(perCorePercentage))
	var cpuSum float64
	for i, pct := range perCorePercentage {
		cpuUsage[i] = gin.H{"core": i, "usage": pct}
		cpuSum += pct
	}
	overallCPU := 0.0
	if len(perCorePercentage) > 0 {
		overallCPU = cpuSum / float64(len(perCorePercentage))
	}
	overAllPercentage := []float64{overallCPU}

	cpuInfo, err := cpu.Info()
	cpuModelName := "Unknown"
	if err == nil && len(cpuInfo) > 0 {
		cpuModelName = cpuInfo[0].ModelName
	}

	hostId, err := host.HostID()
	if err != nil {
		return nil, fmt.Errorf("host id: %w", err)
	}
	sensorsTemps, err := host.SensorsTemperatures()
	if err != nil {
		// sensors may not be available on all platforms — silently ignore
		sensorsTemps = nil
	}

	// 2. RAM usage
	vMem, err := mem.VirtualMemory()
	if err != nil {
		return nil, fmt.Errorf("memory: %w", err)
	}
	ramUsedGB := float64(vMem.Used) / (1024 * 1024 * 1024)
	ramTotalGB := float64(vMem.Total) / (1024 * 1024 * 1024)

	// 3. Disk usage
	d, err := disk.Usage("/")
	diskTotalGB := 0.0
	diskUsedGB := 0.0
	diskFreeGB := 0.0
	if err == nil {
		diskTotalGB = float64(d.Total) / (1024 * 1024 * 1024)
		diskUsedGB = float64(d.Used) / (1024 * 1024 * 1024)
		diskFreeGB = float64(d.Free) / (1024 * 1024 * 1024)
	}

	// 4. Network connections — real listening ports (Port Sentry) and real
	//    inbound established connections (who is connected to this server).
	connections, err := psnet.Connections("tcp")
	listeningSet := map[uint32]bool{}
	listeningPorts := []gin.H{}
	inboundConns := []gin.H{}
	activeConnCount := 0
	if err == nil {
		// First pass: collect the set of ports we listen on (dedup IPv4/IPv6).
		seenPort := map[uint32]bool{}
		for _, conn := range connections {
			if conn.Status == "LISTEN" {
				listeningSet[conn.Laddr.Port] = true
				if !seenPort[conn.Laddr.Port] {
					seenPort[conn.Laddr.Port] = true
					listeningPorts = append(listeningPorts, gin.H{
						"port":    conn.Laddr.Port,
						"service": serviceName(conn.Laddr.Port),
						"open":    true,
					})
				}
			}
		}
		// Second pass: established connections TO one of our listening ports
		// are inbound — someone connected to the server.
		for _, conn := range connections {
			if conn.Status != "ESTABLISHED" {
				continue
			}
			activeConnCount++
			if listeningSet[conn.Laddr.Port] && conn.Raddr.IP != "" {
				if len(inboundConns) < 20 {
					inboundConns = append(inboundConns, gin.H{
						"ip":         conn.Raddr.IP,
						"remote":     fmt.Sprintf("%s:%d", conn.Raddr.IP, conn.Raddr.Port),
						"local_port": conn.Laddr.Port,
						"service":    serviceName(conn.Laddr.Port),
						"timestamp":  time.Now().UTC().Format(time.RFC3339),
						"success":    true,
					})
				}
			}
		}
	}

	// Real log-style lines for the Live Feed, derived from current metrics.
	nowLog := time.Now().UTC().Format("15:04:05")
	logs := []string{
		fmt.Sprintf("[INFO] %s cpu %.0f%% · %d cores", nowLog, overallCPU, len(perCorePercentage)),
		fmt.Sprintf("[INFO] %s mem %.1f/%.1f GB", nowLog, ramUsedGB, ramTotalGB),
		fmt.Sprintf("[INFO] %s net %d established · %d listening", nowLog, activeConnCount, len(listeningPorts)),
	}
	if overallCPU > 90 {
		logs = append(logs, fmt.Sprintf("[WARN] %s cpu pressure high (%.0f%%)", nowLog, overallCPU))
	}

	// Health score
	finalHealthScore := "A"
	if overAllPercentage[0] > 65 {
		finalHealthScore = "B"
	}
	if overAllPercentage[0] > 80 {
		finalHealthScore = "C"
	}
	if overAllPercentage[0] > 90 {
		finalHealthScore = "D"
	}
	if overAllPercentage[0] > 100 {
		finalHealthScore = "E"
	}

	nodeName := "MacBook Pro 16"
	if hn, err := os.Hostname(); err == nil {
		nodeName = hn
	}

	return gin.H{
		"system": gin.H{
			"host_id":      hostId,
			"node_name":    nodeName,
			"ip_address":   getLocalIP(),
			"cpu_model":    cpuModelName,
			"status":       "Online",
			"health_score": finalHealthScore,
		},
		"network": gin.H{
			"signal_dbm":         -38,
			"signal_quality":     "Ultra Stable",
			"vpn_active":         "Tailscale",
			"active_connections": activeConnCount,
			"port_sentry":        listeningPorts,
			"connections":        inboundConns,
		},
		"logs": logs,
		"hardware": gin.H{
			"cpu_usage": cpuUsage,
			"temperatures": gin.H{
				"overall": "N/A",
				"status":  "Nominal",
				"sensors": sensorsTemps,
			},
			"memory": gin.H{
				"used_gb":       ramUsedGB,
				"total_gb":      ramTotalGB,
				"app_memory_gb": ramUsedGB * 0.4,
				"pressure":      "Normal",
			},
			"storage": gin.H{
				"total_gb":     diskTotalGB,
				"system_gb":    diskUsedGB,
				"models_gb":    128.0,
				"available_gb": diskFreeGB,
			},
		},
		"cluster": gin.H{
			"total_ram_gb":     19.7,
			"total_storage_gb": 708.0,
			"active_nodes":     3,
			"total_nodes":      3,
			"ai_instances":     1,
		},
		"ai_engine": gin.H{
			// Inference runs on Cloudflare Workers AI (see the AI Usage page and
			// the Model Library for the active per-feature models). This node does
			// not host a local model, so we report the real backend as remote.
			"status":          "Ready",
			"active_model":    "Cloudflare Workers AI",
			"tokens_per_sec":  0.0,
			"context_used":    0,
			"context_total":   8192,
			"compute_backend": "Cloudflare",
			"terminal_latest": ">_ workers-ai",
			"available_models": []gin.H{},
		},
	}, nil
}

// Get handles GET /api/v1/status — single JSON snapshot.
func (h *StatusHandler) Get(c *gin.Context) {
	data, err := collectStatus()
	if err != nil {
		h.Log.Error("status", fmt.Sprintf("collect error: %v", err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to collect status"})
		return
	}
	c.JSON(http.StatusOK, data)
}

// Stream handles GET /api/v1/status/stream — pushes status snapshots via SSE every 3 seconds.
func (h *StatusHandler) Stream(c *gin.Context) {
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("X-Accel-Buffering", "no")

	// Send initial snapshot immediately
	if data, err := collectStatus(); err == nil {
		if raw, err := json.Marshal(data); err == nil {
			fmt.Fprintf(c.Writer, "data: %s\n\n", raw)
			c.Writer.Flush()
		}
	}

	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	clientGone := c.Request.Context().Done()

	for {
		select {
		case <-clientGone:
			return
		case <-ticker.C:
			data, err := collectStatus()
			if err != nil {
				h.Log.Error("status", fmt.Sprintf("SSE collect error: %v", err))
				continue
			}
			raw, err := json.Marshal(data)
			if err != nil {
				continue
			}
			_, writeErr := fmt.Fprintf(c.Writer, "data: %s\n\n", raw)
			if writeErr != nil {
				return
			}
			c.Writer.Flush()
		}
	}
}
