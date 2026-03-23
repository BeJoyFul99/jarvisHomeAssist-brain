package handlers

import (
	"context"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"jarvishomeassist-brain/internal/models"
	"jarvishomeassist-brain/internal/sse"
	"jarvishomeassist-brain/internal/wiz"
)

// DeviceHandler manages smart-home device CRUD and control.
type DeviceHandler struct {
	DB  *gorm.DB
	Hub *sse.Hub
}

// ── List ──────────────────────────────────────────────────
// GET /api/v1/devices — list all registered smart devices.
func (h *DeviceHandler) List(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
	defer cancel()

	var devices []models.SmartDevice
	query := h.DB.WithContext(ctx).Order("room asc, name asc")

	// Optional filters
	if room := c.Query("room"); room != "" {
		query = query.Where("room = ?", room)
	}
	if brand := c.Query("brand"); brand != "" {
		query = query.Where("brand = ?", brand)
	}
	if deviceType := c.Query("type"); deviceType != "" {
		query = query.Where("device_type = ?", deviceType)
	}

	if err := query.Find(&devices).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load devices"})
		return
	}

	c.JSON(http.StatusOK, devices)
}

// ── Get ──────────────────────────────────────────────────
// GET /api/v1/devices/:id — get a single device.
func (h *DeviceHandler) Get(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
	defer cancel()

	var device models.SmartDevice
	if err := h.DB.WithContext(ctx).First(&device, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "device not found"})
		return
	}

	c.JSON(http.StatusOK, device)
}

// ── Create ──────────────────────────────────────────────────
// POST /api/v1/admin/devices — register a new smart device.
func (h *DeviceHandler) Create(c *gin.Context) {
	var body struct {
		Name        string `json:"name" binding:"required"`
		Room        string `json:"room" binding:"required"`
		DeviceType  string `json:"device_type"`
		Brand       string `json:"brand"`
		Model       string `json:"model"`
		IP          string `json:"ip" binding:"required"`
		MAC         string `json:"mac"`
		FirmwareVer string `json:"firmware_ver"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	device := models.SmartDevice{
		Name:        body.Name,
		Room:        body.Room,
		DeviceType:  cond(body.DeviceType != "", body.DeviceType, "light"),
		Brand:       cond(body.Brand != "", body.Brand, "wiz"),
		Model:       body.Model,
		IP:          body.IP,
		MAC:         body.MAC,
		FirmwareVer: body.FirmwareVer,
		Online:      false,
		State:       models.JSON{"on": false, "brightness": 100},
		Metadata:    models.JSON{},
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
	defer cancel()

	if err := h.DB.WithContext(ctx).Create(&device).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create device"})
		return
	}

	// Try to auto-detect WiZ bulb info
	if device.Brand == "wiz" {
		go h.probeWizDevice(device.ID, device.IP)
	}

	h.Hub.Broadcast(sse.Event{Type: "device:created", Data: device})
	c.JSON(http.StatusCreated, device)
}

// ── Update ──────────────────────────────────────────────────
// PATCH /api/v1/admin/devices/:id — update device metadata.
func (h *DeviceHandler) Update(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}

	var body struct {
		Name        *string `json:"name"`
		Room        *string `json:"room"`
		DeviceType  *string `json:"device_type"`
		Brand       *string `json:"brand"`
		Model       *string `json:"model"`
		IP          *string `json:"ip"`
		MAC         *string `json:"mac"`
		FirmwareVer *string `json:"firmware_ver"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
	defer cancel()

	var device models.SmartDevice
	if err := h.DB.WithContext(ctx).First(&device, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "device not found"})
		return
	}

	if body.Name != nil {
		device.Name = *body.Name
	}
	if body.Room != nil {
		device.Room = *body.Room
	}
	if body.DeviceType != nil {
		device.DeviceType = *body.DeviceType
	}
	if body.Brand != nil {
		device.Brand = *body.Brand
	}
	if body.Model != nil {
		device.Model = *body.Model
	}
	if body.IP != nil {
		device.IP = *body.IP
	}
	if body.MAC != nil {
		device.MAC = *body.MAC
	}
	if body.FirmwareVer != nil {
		device.FirmwareVer = *body.FirmwareVer
	}

	if err := h.DB.WithContext(ctx).Save(&device).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update device"})
		return
	}

	h.Hub.Broadcast(sse.Event{Type: "device:updated", Data: device})
	c.JSON(http.StatusOK, device)
}

// ── Delete ──────────────────────────────────────────────────
// DELETE /api/v1/admin/devices/:id — remove a device.
func (h *DeviceHandler) Delete(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
	defer cancel()

	if err := h.DB.WithContext(ctx).Delete(&models.SmartDevice{}, id).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete device"})
		return
	}

	h.Hub.Broadcast(sse.Event{Type: "device:deleted", Data: gin.H{"id": id}})
	c.JSON(http.StatusOK, gin.H{"message": "deleted"})
}

// ── Control ──────────────────────────────────────────────────
// POST /api/v1/devices/:id/control — send a command to a device.
// Body: { "action": "on"|"off"|"brightness"|"color_temp"|"rgb"|"scene", ...params }
func (h *DeviceHandler) Control(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}

	var body struct {
		Action     string `json:"action" binding:"required"`
		Brightness *int   `json:"brightness"`
		ColorTemp  *int   `json:"color_temp"`
		R          *int   `json:"r"`
		G          *int   `json:"g"`
		B          *int   `json:"b"`
		SceneID    *int   `json:"scene_id"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
	defer cancel()

	var device models.SmartDevice
	if err := h.DB.WithContext(ctx).First(&device, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "device not found"})
		return
	}

	if device.Brand != "wiz" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "control only supported for WiZ devices"})
		return
	}

	var wizResp *wiz.Response
	var wizErr error

	switch body.Action {
	case "on":
		wizResp, wizErr = wiz.TurnOn(device.IP)
		if wizErr == nil {
			device.State["on"] = true
		}

	case "off":
		wizResp, wizErr = wiz.TurnOff(device.IP)
		if wizErr == nil {
			device.State["on"] = false
		}

	case "brightness":
		if body.Brightness == nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "brightness required (10-100)"})
			return
		}
		wizResp, wizErr = wiz.SetBrightness(device.IP, *body.Brightness)
		if wizErr == nil {
			device.State["on"] = true
			device.State["brightness"] = *body.Brightness
		}

	case "color_temp":
		if body.ColorTemp == nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "color_temp required (2200-6500)"})
			return
		}
		wizResp, wizErr = wiz.SetColorTemp(device.IP, *body.ColorTemp)
		if wizErr == nil {
			device.State["on"] = true
			device.State["color_temp"] = *body.ColorTemp
			// Clear RGB when switching to color temp mode
			delete(device.State, "r")
			delete(device.State, "g")
			delete(device.State, "b")
		}

	case "rgb":
		if body.R == nil || body.G == nil || body.B == nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "r, g, b required (0-255)"})
			return
		}
		dimming := 100
		if body.Brightness != nil {
			dimming = *body.Brightness
		}
		wizResp, wizErr = wiz.SetRGB(device.IP, *body.R, *body.G, *body.B, dimming)
		if wizErr == nil {
			device.State["on"] = true
			device.State["r"] = *body.R
			device.State["g"] = *body.G
			device.State["b"] = *body.B
			device.State["brightness"] = dimming
			delete(device.State, "color_temp")
		}

	case "scene":
		if body.SceneID == nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "scene_id required"})
			return
		}
		wizResp, wizErr = wiz.SetScene(device.IP, *body.SceneID)
		if wizErr == nil {
			device.State["on"] = true
			device.State["scene_id"] = *body.SceneID
			if name, ok := wiz.WizScenes[*body.SceneID]; ok {
				device.State["scene_name"] = name
			}
		}

	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "unknown action: " + body.Action})
		return
	}

	if wizErr != nil {
		log.Printf("[wiz] control %s (device %d @ %s): %v", body.Action, device.ID, device.IP, wizErr)
		c.JSON(http.StatusBadGateway, gin.H{"error": "device unreachable", "detail": wizErr.Error()})
		return
	}

	device.Online = true
	h.DB.Save(&device)
	h.Hub.Broadcast(sse.Event{Type: "device:updated", Data: device})

	c.JSON(http.StatusOK, gin.H{
		"device":   device,
		"wiz_resp": wizResp,
	})
}

// ── State ──────────────────────────────────────────────────
// GET /api/v1/devices/:id/state — poll current state from the physical device.
func (h *DeviceHandler) State(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
	defer cancel()

	var device models.SmartDevice
	if err := h.DB.WithContext(ctx).First(&device, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "device not found"})
		return
	}

	if device.Brand != "wiz" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "state polling only supported for WiZ devices"})
		return
	}

	resp, err := wiz.GetPilot(device.IP)
	if err != nil {
		device.Online = false
		h.DB.Save(&device)
		c.JSON(http.StatusBadGateway, gin.H{"error": "device unreachable", "online": false})
		return
	}

	// Sync state from bulb response
	device.Online = true
	if resp.Result != nil {
		if state, ok := resp.Result["state"].(bool); ok {
			device.State["on"] = state
		}
		if dimming, ok := resp.Result["dimming"].(float64); ok {
			device.State["brightness"] = int(dimming)
		}
		if temp, ok := resp.Result["temp"].(float64); ok {
			device.State["color_temp"] = int(temp)
		}
		if r, ok := resp.Result["r"].(float64); ok {
			device.State["r"] = int(r)
		}
		if g, ok := resp.Result["g"].(float64); ok {
			device.State["g"] = int(g)
		}
		if b, ok := resp.Result["b"].(float64); ok {
			device.State["b"] = int(b)
		}
		if sceneId, ok := resp.Result["sceneId"].(float64); ok {
			device.State["scene_id"] = int(sceneId)
			if name, exists := wiz.WizScenes[int(sceneId)]; exists {
				device.State["scene_name"] = name
			}
		}
	}

	h.DB.Save(&device)
	h.Hub.Broadcast(sse.Event{Type: "device:updated", Data: device})

	c.JSON(http.StatusOK, device)
}

// ── Discover ──────────────────────────────────────────────────
// POST /api/v1/admin/devices/discover — scan the local network for WiZ bulbs.
func (h *DeviceHandler) Discover(c *gin.Context) {
	localIP := getLocalIP()
	if localIP == "" {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "could not determine local IP"})
		return
	}

	ips := wiz.Discover(localIP, 3*time.Second)

	// Check which IPs are already registered
	ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
	defer cancel()

	var existing []models.SmartDevice
	h.DB.WithContext(ctx).Where("brand = ?", "wiz").Find(&existing)

	existingIPs := map[string]bool{}
	for _, d := range existing {
		existingIPs[d.IP] = true
	}

	type discoveredBulb struct {
		IP         string `json:"ip"`
		Registered bool   `json:"registered"`
		MAC        string `json:"mac,omitempty"`
		Module     string `json:"module,omitempty"`
	}

	results := make([]discoveredBulb, 0, len(ips))
	for _, ip := range ips {
		bulb := discoveredBulb{IP: ip, Registered: existingIPs[ip]}

		// Try to get system config for extra info
		resp, err := wiz.GetSystemConfig(ip)
		if err == nil && resp.Result != nil {
			if mac, ok := resp.Result["mac"].(string); ok {
				bulb.MAC = mac
			}
			if module, ok := resp.Result["moduleName"].(string); ok {
				bulb.Module = module
			}
		}

		results = append(results, bulb)
	}

	c.JSON(http.StatusOK, gin.H{
		"local_ip":   localIP,
		"discovered": results,
		"count":      len(results),
	})
}

// ── Scenes ──────────────────────────────────────────────────
// GET /api/v1/devices/scenes — list available WiZ scenes.
func (h *DeviceHandler) Scenes(c *gin.Context) {
	type scene struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	}
	scenes := make([]scene, 0, len(wiz.WizScenes))
	for id, name := range wiz.WizScenes {
		scenes = append(scenes, scene{ID: id, Name: name})
	}
	c.JSON(http.StatusOK, scenes)
}

// probeWizDevice fetches system config from a WiZ bulb and updates the DB record.
func (h *DeviceHandler) probeWizDevice(deviceID uint, ip string) {
	resp, err := wiz.GetSystemConfig(ip)
	if err != nil {
		log.Printf("[wiz] probe %s failed: %v", ip, err)
		return
	}

	updates := map[string]interface{}{"online": true}
	meta := models.JSON{}

	if resp.Result != nil {
		if mac, ok := resp.Result["mac"].(string); ok {
			updates["mac"] = mac
		}
		if fwVersion, ok := resp.Result["fwVersion"].(string); ok {
			updates["firmware_ver"] = fwVersion
		}
		if moduleName, ok := resp.Result["moduleName"].(string); ok {
			meta["module_name"] = moduleName
		}
		if homeID, ok := resp.Result["homeId"].(float64); ok {
			meta["home_id"] = int(homeID)
		}
	}

	if len(meta) > 0 {
		updates["metadata"] = meta
	}

	h.DB.Model(&models.SmartDevice{}).Where("id = ?", deviceID).Updates(updates)

	// Also get initial pilot state
	pilotResp, err := wiz.GetPilot(ip)
	if err == nil && pilotResp.Result != nil {
		state := models.JSON{}
		if s, ok := pilotResp.Result["state"].(bool); ok {
			state["on"] = s
		}
		if d, ok := pilotResp.Result["dimming"].(float64); ok {
			state["brightness"] = int(d)
		}
		if t, ok := pilotResp.Result["temp"].(float64); ok {
			state["color_temp"] = int(t)
		}
		if len(state) > 0 {
			h.DB.Model(&models.SmartDevice{}).Where("id = ?", deviceID).Update("state", state)
		}
	}

	log.Printf("[wiz] probed device %d @ %s successfully", deviceID, ip)
}

// SeedDefaultDevices creates a sample WiZ bulb entry if no devices exist.
func SeedDefaultDevices(db *gorm.DB) {
	var count int64
	db.Model(&models.SmartDevice{}).Count(&count)
	if count > 0 {
		return
	}

	defaults := []models.SmartDevice{
		{
			Name:       "Dining Room Light",
			Room:       "Dining Room",
			DeviceType: "light",
			Brand:      "wiz",
			Model:      "WiZ A60 Color",
			IP:         "192.168.1.150",
			MAC:        "",
			Online:     false,
			State:      models.JSON{"on": false, "brightness": 100},
			Metadata:   models.JSON{"note": "WiZ smart bulb — update IP after setup"},
		},
	}

	for _, d := range defaults {
		if err := db.Create(&d).Error; err != nil {
			log.Printf("[seed] device %s: %v", d.Name, err)
		} else {
			log.Printf("[seed] device %s created (ip: %s)", d.Name, d.IP)
		}
	}
}
