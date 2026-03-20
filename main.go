package main

import (
	"log"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"
	"github.com/shirou/gopsutil/v3/host"
	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/mem"
	psnet "github.com/shirou/gopsutil/v4/net"

	"jarvishomeassist-brain/internal/config"
	"jarvishomeassist-brain/internal/database"
	"jarvishomeassist-brain/internal/handlers"
	"jarvishomeassist-brain/internal/middleware"
)

// getLocalIP returns the non loopback local IP of the host
func getLocalIP() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "Unknown"
	}
	for _, address := range addrs {
		// check the address type and if it is not a loopback the display it
		if ipnet, ok := address.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
			if ipnet.IP.To4() != nil {
				return ipnet.IP.String()
			}
		}
	}
	return "Unknown"
}

func main() {
	err := godotenv.Load()
	if err != nil {
		log.Println(err)
	}

	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("[config] %v", err)
	}

	// Connect to PostgreSQL
	db, err := database.Connect(cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("[db] %v", err)
	}
	log.Println("[db] connected to PostgreSQL")

	// Run migrations & seed
	if err := database.Migrate(db); err != nil {
		log.Fatalf("[db] %v", err)
	}
	handlers.SeedDefaultUsers(db)

	// Initialize the Gin router
	r := gin.Default()

	// ── Auth routes (public) ────────────────────────────────
	auth := &handlers.AuthHandler{DB: db, Cfg: cfg}
	r.POST("/auth/login", auth.Login)
	r.POST("/auth/pin-login", auth.PINLogin)
	r.POST("/auth/reset-password", auth.ResetPassword)

	// ── Protected route group (require valid JWT) ────────────
	protected := r.Group("/api/v1")
	protected.Use(middleware.JWTAuth(cfg.JWTSecret))

	// ── Admin user management (JWT + administrator role) ─────
	adminUsers := &handlers.AdminUserHandler{DB: db}
	admin := protected.Group("/admin")
	admin.Use(middleware.RequireRole("administrator"))

	admin.GET("/users", adminUsers.ListUsers)
	admin.POST("/users", adminUsers.CreateUser)
	admin.PATCH("/users/:id", adminUsers.UpdateUser)
	admin.DELETE("/users/:id", adminUsers.DeleteUser)
	admin.POST("/users/:id/lock", adminUsers.LockUser)
	admin.POST("/users/:id/revoke", adminUsers.RevokeTokens)
	admin.POST("/users/:id/reset-password", adminUsers.RequestPasswordReset)
	admin.GET("/permissions/schema", adminUsers.PermissionsSchema)

	// ── System status (internal / server-to-server) ─────────
	// This endpoint is called by the Next.js API proxy (server-side),
	// not by browsers directly. Auth is enforced at the Next.js layer.
	r.GET("/api/v1/status", func(c *gin.Context) {
		// 1. Get CPU usage (sampled over 500ms)
		overAllPercentage, err := cpu.Percent(500*time.Millisecond, false)
		if err != nil {
			log.Println(err)
			return
		}
		var cores []float64
		perCorePercentage, err := cpu.Percent(500*time.Millisecond, true)
		if err != nil {
			log.Println(err)
			return
		}
		for i := range perCorePercentage {
			cores = append(cores, perCorePercentage[i])
		}
		cpuUsage := []gin.H{}
		for i := range cores {
			cpuUsage = append(cpuUsage, gin.H{
				"core":  i,
				"usage": cores[i],
			})
		}

		cpuInfo, err := cpu.Info()
		cpuModelName := "Unknown"
		if err == nil && len(cpuInfo) > 0 {
			cpuModelName = cpuInfo[0].ModelName
		}

		hostId, err := host.HostID()
		if err != nil {
			log.Println(err)
			return
		}
		sensorsTemps, err := host.SensorsTemperatures()
		if err != nil {
			log.Println(err)
			return
		}

		// 2. Get RAM usage stats
		vMem, err := mem.VirtualMemory()
		if err != nil {
			log.Println(err)
			return
		}
		ramUsedGB := float64(vMem.Used) / (1024 * 1024 * 1024)
		ramTotalGB := float64(vMem.Total) / (1024 * 1024 * 1024)

		// 3. Get Disk usage stats
		d, err := disk.Usage("/")
		diskTotalGB := 0.0
		diskUsedGB := 0.0
		diskFreeGB := 0.0
		if err == nil {
			diskTotalGB = float64(d.Total) / (1024 * 1024 * 1024)
			diskUsedGB = float64(d.Used) / (1024 * 1024 * 1024)
			diskFreeGB = float64(d.Free) / (1024 * 1024 * 1024)
		}

		// 4. Get Network Connections & Ports (Port Sentry & Active Connections)
		// We only scan tcp for this simplified dashboard view.
		connections, err := psnet.Connections("tcp")
		listeningPorts := []gin.H{}
		activeConnCount := 0
		if err == nil {
			for _, conn := range connections {
				if conn.Status == "LISTEN" {
					// Add to port sentry list
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

		// 5. Send the JSON response to the frontend
		c.JSON(http.StatusOK, gin.H{
			"system": gin.H{
				"host_id":      hostId,
				"node_name":    nodeName,
				"ip_address":   getLocalIP(),
				"cpu_model":    cpuModelName,
				"status":       "Online",
				"health_score": finalHealthScore,
			},
			"network": gin.H{
				"signal_dbm":         -38,             // Mocked for now
				"signal_quality":     "Ultra Stable",  // Mocked for now
				"vpn_active":         "Tailscale",     // Mocked for now
				"active_connections": activeConnCount, // Real data
				"port_sentry":        listeningPorts,  // Real data
			},
			"hardware": gin.H{
				"cpu_usage": cpuUsage,
				"temperatures": gin.H{
					"overall": "N/A", // Can aggregate from sensors later
					"status":  "Nominal",
					"sensors": sensorsTemps,
				},
				"memory": gin.H{
					"used_gb":       ramUsedGB,
					"total_gb":      ramTotalGB,
					"app_memory_gb": ramUsedGB * 0.4, // Rough mock split of app vs system mem
					"pressure":      "Normal",
				},
				"storage": gin.H{
					"total_gb":     diskTotalGB,
					"system_gb":    diskUsedGB,
					"models_gb":    128.0, // Mocked for now (could calculate from a models folder)
					"available_gb": diskFreeGB,
				},
			},
			"cluster": gin.H{
				"total_ram_gb":     19.7,  // Mocked global tracking
				"total_storage_gb": 708.0, // Mocked global tracking
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
		})
	})

	// Start the server on port 5000
	r.Run(":" + cfg.Port)
}
