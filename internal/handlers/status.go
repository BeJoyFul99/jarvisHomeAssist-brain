package handlers

import (
	"encoding/json"
	"fmt"
	"log"
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
)

// StatusHandler serves system status as JSON or SSE stream.
type StatusHandler struct{}

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
	// 1. CPU usage (sampled over 500ms)
	overAllPercentage, err := cpu.Percent(500*time.Millisecond, false)
	if err != nil {
		return nil, fmt.Errorf("cpu overall: %w", err)
	}
	perCorePercentage, err := cpu.Percent(500*time.Millisecond, true)
	if err != nil {
		return nil, fmt.Errorf("cpu per-core: %w", err)
	}

	cpuUsage := make([]gin.H, len(perCorePercentage))
	for i, pct := range perCorePercentage {
		cpuUsage[i] = gin.H{"core": i, "usage": pct}
	}

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
		log.Printf("[status] sensors: %v", err)
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

	// 4. Network connections
	connections, err := psnet.Connections("tcp")
	listeningPorts := []gin.H{}
	activeConnCount := 0
	if err == nil {
		for _, conn := range connections {
			if conn.Status == "LISTEN" {
				listeningPorts = append(listeningPorts, gin.H{
					"port":   conn.Laddr.Port,
					"ip":     conn.Laddr.IP,
					"family": conn.Family,
				})
			} else if conn.Status == "ESTABLISHED" {
				activeConnCount++
			}
		}
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
		},
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
			"status":          "Inferring",
			"active_model":    "Mistral-7B-v0.3-Q4_K_M.gguf",
			"tokens_per_sec":  0.0,
			"context_used":    0,
			"context_total":   8192,
			"compute_backend": "CPU",
			"terminal_latest": ">_ llama.cpp prompt",
			"available_models": []gin.H{
				{
					"name":         "Mistral-7B-v0.3-Q4_K_M.gguf",
					"size_gb":      4.1,
					"quantization": "Q4_K_M",
					"is_active":    true,
				},
				{
					"name":         "Llama-3-8B-Q3_K_S.gguf",
					"size_gb":      3.1,
					"quantization": "Q3_K_S",
					"is_active":    false,
				},
				{
					"name":         "Phi-3-mini-Q5_K_M.gguf",
					"size_gb":      1.1,
					"quantization": "Q5_K_M",
					"is_active":    false,
				},
			},
		},
	}, nil
}

// Get handles GET /api/v1/status — single JSON snapshot.
func (h *StatusHandler) Get(c *gin.Context) {
	data, err := collectStatus()
	if err != nil {
		log.Printf("[status] collect error: %v", err)
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
				log.Printf("[status-sse] collect error: %v", err)
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
