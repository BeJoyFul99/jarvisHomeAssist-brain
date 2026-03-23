package wiz

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"time"
)

const (
	WizPort    = 38899
	UDPTimeout = 2 * time.Second
)

// Request is the JSON structure sent to a WiZ bulb over UDP.
type Request struct {
	Method string      `json:"method"`
	Params interface{} `json:"params,omitempty"`
}

// Response is the JSON structure received from a WiZ bulb.
type Response struct {
	Method string                 `json:"method"`
	Env    string                 `json:"env,omitempty"`
	Result map[string]interface{} `json:"result,omitempty"`
	Error  *ResponseError         `json:"error,omitempty"`
}

type ResponseError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// PilotState represents the state sent/received via setPilot/getPilot.
type PilotState struct {
	State   *bool  `json:"state,omitempty"`
	Dimming *int   `json:"dimming,omitempty"` // 10–100
	Temp    *int   `json:"temp,omitempty"`    // color temperature 2200–6500K
	R       *int   `json:"r,omitempty"`       // 0–255
	G       *int   `json:"g,omitempty"`
	B       *int   `json:"b,omitempty"`
	SceneID *int   `json:"sceneId,omitempty"` // built-in scene ID
	Speed   *int   `json:"speed,omitempty"`   // dynamic scene speed 20–200
}

// Send sends a UDP JSON message to a WiZ bulb and returns the response.
func Send(ip string, req Request) (*Response, error) {
	addr := fmt.Sprintf("%s:%d", ip, WizPort)
	conn, err := net.DialTimeout("udp", addr, UDPTimeout)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", addr, err)
	}
	defer conn.Close()

	data, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}

	conn.SetDeadline(time.Now().Add(UDPTimeout))

	if _, err := conn.Write(data); err != nil {
		return nil, fmt.Errorf("write: %w", err)
	}

	buf := make([]byte, 2048)
	n, err := conn.Read(buf)
	if err != nil {
		return nil, fmt.Errorf("read: %w", err)
	}

	var resp Response
	if err := json.Unmarshal(buf[:n], &resp); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}

	if resp.Error != nil {
		return &resp, fmt.Errorf("wiz error %d: %s", resp.Error.Code, resp.Error.Message)
	}

	return &resp, nil
}

// GetPilot queries the current state of the bulb.
func GetPilot(ip string) (*Response, error) {
	return Send(ip, Request{Method: "getPilot", Params: map[string]interface{}{}})
}

// SetPilot sets the state of the bulb (on/off, brightness, color, scene).
func SetPilot(ip string, state PilotState) (*Response, error) {
	return Send(ip, Request{Method: "setPilot", Params: state})
}

// TurnOn turns the bulb on.
func TurnOn(ip string) (*Response, error) {
	on := true
	return SetPilot(ip, PilotState{State: &on})
}

// TurnOff turns the bulb off.
func TurnOff(ip string) (*Response, error) {
	off := false
	return SetPilot(ip, PilotState{State: &off})
}

// SetBrightness sets brightness (10–100).
func SetBrightness(ip string, dimming int) (*Response, error) {
	on := true
	return SetPilot(ip, PilotState{State: &on, Dimming: &dimming})
}

// SetColorTemp sets warm/cool white (2200–6500K).
func SetColorTemp(ip string, temp int) (*Response, error) {
	on := true
	return SetPilot(ip, PilotState{State: &on, Temp: &temp})
}

// SetRGB sets an RGB color.
func SetRGB(ip string, r, g, b, dimming int) (*Response, error) {
	on := true
	return SetPilot(ip, PilotState{State: &on, R: &r, G: &g, B: &b, Dimming: &dimming})
}

// SetScene activates a built-in WiZ scene by ID.
func SetScene(ip string, sceneID int) (*Response, error) {
	on := true
	return SetPilot(ip, PilotState{State: &on, SceneID: &sceneID})
}

// GetSystemConfig gets the bulb's system configuration (MAC, firmware, module, etc.).
func GetSystemConfig(ip string) (*Response, error) {
	return Send(ip, Request{Method: "getSystemConfig", Params: map[string]interface{}{}})
}

// Ping checks if a WiZ bulb is reachable by sending a getPilot and checking for response.
func Ping(ip string) bool {
	_, err := GetPilot(ip)
	return err == nil
}

// Discover broadcasts a registration message on the local network to find WiZ bulbs.
// Returns a list of responding IPs.
func Discover(localIP string, timeout time.Duration) []string {
	addr, err := net.ResolveUDPAddr("udp4", fmt.Sprintf("255.255.255.255:%d", WizPort))
	if err != nil {
		log.Printf("[wiz] resolve broadcast addr: %v", err)
		return nil
	}

	conn, err := net.DialUDP("udp4", nil, addr)
	if err != nil {
		log.Printf("[wiz] dial broadcast: %v", err)
		return nil
	}
	defer conn.Close()

	msg, _ := json.Marshal(Request{
		Method: "registration",
		Params: map[string]interface{}{
			"phoneMac": "AAAAAAAAAAAA",
			"register": false,
			"phoneIp":  localIP,
			"id":       "1",
		},
	})

	conn.SetDeadline(time.Now().Add(timeout))
	conn.Write(msg)

	found := map[string]bool{}
	buf := make([]byte, 2048)

	for {
		n, remoteAddr, err := conn.ReadFromUDP(buf)
		if err != nil {
			break // timeout or error
		}

		var resp Response
		if json.Unmarshal(buf[:n], &resp) == nil {
			ip := remoteAddr.IP.String()
			if !found[ip] {
				found[ip] = true
				log.Printf("[wiz] discovered bulb at %s", ip)
			}
		}
	}

	ips := make([]string, 0, len(found))
	for ip := range found {
		ips = append(ips, ip)
	}
	return ips
}

// WizScenes maps scene IDs to human-readable names.
var WizScenes = map[int]string{
	1:  "Ocean",
	2:  "Romance",
	3:  "Sunset",
	4:  "Party",
	5:  "Fireplace",
	6:  "Cozy",
	7:  "Forest",
	8:  "Pastel Colors",
	9:  "Wake Up",
	10: "Bedtime",
	11: "Warm White",
	12: "Daylight",
	13: "Cool White",
	14: "Night Light",
	15: "Focus",
	16: "Relax",
	17: "True Colors",
	18: "TV Time",
	19: "Plant Growth",
	20: "Spring",
	21: "Summer",
	22: "Fall",
	23: "Deep Dive",
	24: "Jungle",
	25: "Mojito",
	26: "Club",
	27: "Christmas",
	28: "Halloween",
	29: "Candlelight",
	30: "Golden White",
	31: "Pulse",
	32: "Steampunk",
}
