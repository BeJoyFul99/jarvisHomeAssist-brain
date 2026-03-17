package main

import (
	"net/http"
	"time"

	// Type the Gin URL here:
	"github.com/gin-gonic/gin"
	// Type the CPU URL here:
	"github.com/shirou/gopsutil/v4/cpu"
	// Type the RAM URL here:
	"github.com/shirou/gopsutil/v4/mem"
)

func main() {
	// Initialize the Gin router
	r := gin.Default()

	// This is the API endpoint for your Lovable dashboard
	r.GET("/api/v1/status", func(c *gin.Context) {
		
		// 1. Get CPU usage (sampled over 500ms)
		cpuPercent, _ := cpu.Percent(500*time.Millisecond, false)

		// 2. Get RAM usage stats for your 16GB LPDDR4X
		vMem, _ := mem.VirtualMemory()

		// 3. Calculate "Health Score" logic for your Intel i7
		healthScore := "A"
		var currentCPU float64
		if len(cpuPercent) > 0 {
			currentCPU = cpuPercent[0]
			if currentCPU > 80 || vMem.UsedPercent > 90 {
				healthScore = "D"
			} else if currentCPU > 50 {
				healthScore = "B"
			}
		}

		// 4. Send the JSON response to the frontend
		c.JSON(http.StatusOK, gin.H{
			"node_name":    "Jarvis-MBA-2020",
			"cpu_usage":    currentCPU,
			"ram_used_gb":  float64(vMem.Used) / (1024 * 1024 * 1024),
			"ram_total_gb": float64(vMem.Total) / (1024 * 1024 * 1024),
			"health_score": healthScore,
			"status":       "Online",
		})
	})

	// Start the server on port 8080
	r.Run(":8080")
}

