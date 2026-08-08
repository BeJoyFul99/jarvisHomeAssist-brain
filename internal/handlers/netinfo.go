package handlers

import (
	"bufio"
	"bytes"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// wifiInfo is a best-effort read of the host's WiFi signal. OK is false when
// the platform can't be queried (e.g. wired-only, or running in a container
// with no WiFi interface) — the UI then shows "Unavailable" rather than a
// fabricated value.
type wifiInfo struct {
	SignalDBM int
	Quality   string
	SSID      string
	OK        bool
}

// LANDevice is a device on the local network. When a router client is
// configured it comes from the gateway's full client list (with names);
// otherwise it's an ARP neighbor the host has recently talked to.
type LANDevice struct {
	Name      string `json:"name,omitempty"`
	IP        string `json:"ip"`
	MAC       string `json:"mac"`
	Interface string `json:"interface,omitempty"`
	Active    bool   `json:"active"`
	Source    string `json:"source"` // "router" | "arp"
}

// RouterProvider, when set at startup, supplies the authoritative connected-
// device list from the home gateway. Returns nil,err when unavailable so the
// caller falls back to ARP.
var RouterProvider func() ([]LANDevice, error)

// AppSession is a currently signed-in app user (active JWT).
type AppSession struct {
	Name string
	Role string
	When string // last-login time "HH:MM:SS", empty if unknown
}

// SessionsProvider, when set at startup, lists users with an active session.
// Backed by the DB in main.go so this package stays DB-free.
var SessionsProvider func() []AppSession

// BlockProvider, when set, blocks/unblocks a device by MAC at the router.
// Returns an error (surfaced to the admin) when unavailable or unconfigured.
var BlockProvider func(mac string, block bool) error

// BlockDevice handles POST /api/v1/admin/network/block {mac, block}.
func BlockDevice(c *gin.Context) {
	var body struct {
		MAC   string `json:"mac" binding:"required"`
		Block bool   `json:"block"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "mac is required"})
		return
	}
	if BlockProvider == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error": "router blocking is not configured (set ROUTER_PASSWORD and ROUTER_BLOCK_XPATH)",
		})
		return
	}
	if err := BlockProvider(body.MAC, body.Block); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// Reading WiFi/ARP shells out and can take ~1s, so cache it; the status
// ticker fires every 3s but this refreshes at most every netCacheTTL.
var (
	netMu       sync.Mutex
	wifiCache   wifiInfo
	lanCache    []LANDevice
	netCachedAt time.Time
)

const netCacheTTL = 20 * time.Second

// cachedNetInfo returns the WiFi signal and LAN neighbor list, refreshing at
// most once per netCacheTTL. Exec happens outside the lock so concurrent SSE
// streams don't block each other.
func cachedNetInfo() (wifiInfo, []LANDevice) {
	netMu.Lock()
	stale := netCachedAt.IsZero() || time.Since(netCachedAt) > netCacheTTL
	w, l := wifiCache, lanCache
	netMu.Unlock()
	if !stale {
		return w, l
	}

	w = readWifiSignal()
	l = readLANDevices()

	netMu.Lock()
	wifiCache, lanCache, netCachedAt = w, l, time.Now()
	netMu.Unlock()
	return w, l
}

func signalQuality(dbm int) string {
	switch {
	case dbm >= -50:
		return "Excellent"
	case dbm >= -60:
		return "Good"
	case dbm >= -67:
		return "Fair"
	case dbm >= -75:
		return "Weak"
	default:
		return "Very Weak"
	}
}

func readWifiSignal() wifiInfo {
	switch runtime.GOOS {
	case "darwin":
		return readWifiDarwin()
	case "linux":
		return readWifiLinux()
	default:
		return wifiInfo{OK: false}
	}
}

var reDarwinSignal = regexp.MustCompile(`Signal / Noise:\s*(-?\d+)\s*dBm`)
var reDarwinSSID = regexp.MustCompile(`(?m)Current Network Information:\s*\n\s+([^\n:]+):`)

func readWifiDarwin() wifiInfo {
	out, err := exec.Command("system_profiler", "SPAirPortDataType").Output()
	if err != nil {
		return wifiInfo{OK: false}
	}
	m := reDarwinSignal.FindSubmatch(out)
	if m == nil {
		return wifiInfo{OK: false}
	}
	dbm, err := strconv.Atoi(string(m[1]))
	if err != nil {
		return wifiInfo{OK: false}
	}
	ssid := ""
	if sm := reDarwinSSID.FindSubmatch(out); sm != nil {
		ssid = strings.TrimSpace(string(sm[1]))
	}
	return wifiInfo{SignalDBM: dbm, Quality: signalQuality(dbm), SSID: ssid, OK: true}
}

func readWifiLinux() wifiInfo {
	data, err := os.ReadFile("/proc/net/wireless")
	if err != nil {
		return wifiInfo{OK: false}
	}
	sc := bufio.NewScanner(bytes.NewReader(data))
	lineNo := 0
	for sc.Scan() {
		lineNo++
		if lineNo <= 2 {
			continue // two header rows
		}
		fields := strings.Fields(sc.Text())
		// iface: status link level noise ...  → level (dBm) is index 3
		if len(fields) < 4 {
			continue
		}
		lvl := strings.TrimSuffix(fields[3], ".")
		f, err := strconv.ParseFloat(lvl, 64)
		if err != nil {
			continue
		}
		dbm := int(f)
		return wifiInfo{SignalDBM: dbm, Quality: signalQuality(dbm), OK: true}
	}
	return wifiInfo{OK: false}
}

var reArp = regexp.MustCompile(`\(([\d.]+)\) at ([0-9a-fA-F:]+)`)

func readLANDevices() []LANDevice {
	// Prefer the router's authoritative list (has device names, full clients).
	if RouterProvider != nil {
		if devs, err := RouterProvider(); err == nil && len(devs) > 0 {
			return devs
		}
	}
	return readARPDevices()
}

// readARPDevices never returns nil — "no devices found" is an empty list, and
// a nil slice would reach the status payload as JSON `null` instead of `[]`.
// Windows always takes the first branch, so this is the common path here.
func readARPDevices() []LANDevice {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		return []LANDevice{}
	}
	out, err := exec.Command("arp", "-a").Output()
	if err != nil {
		return []LANDevice{}
	}
	devs := []LANDevice{}
	seen := map[string]bool{}
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		line := sc.Text()
		if strings.Contains(line, "incomplete") {
			continue
		}
		m := reArp.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		ip, mac := m[1], strings.ToLower(m[2])
		if seen[ip] {
			continue
		}
		// Skip broadcast / multicast noise.
		if strings.HasPrefix(mac, "ff:ff") || strings.HasPrefix(ip, "224.") ||
			strings.HasPrefix(ip, "239.") || strings.HasSuffix(ip, ".255") {
			continue
		}
		seen[ip] = true
		devs = append(devs, LANDevice{IP: ip, MAC: mac, Active: true, Source: "arp"})
		if len(devs) >= 50 {
			break
		}
	}
	return devs
}
