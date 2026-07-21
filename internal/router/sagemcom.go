// Package router talks to a Sagemcom F@st home gateway's local JSON API to
// list connected devices. The Bell Giga Hub (Home Hub 4000 / F@st 5689E/5690)
// is a rebranded Sagemcom F@st and uses this protocol with SHA512 auth.
//
// Protocol mirrored from the community library iMicknl/python-sagemcom-api.
package router

import (
	"bytes"
	"crypto/md5"
	"crypto/sha512"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const apiEndpoint = "/cgi/json-req"

// Host is a device connected to the gateway.
type Host struct {
	UID       int    `json:"uid"`
	Name      string `json:"name"`
	IP        string `json:"ip"`
	MAC       string `json:"mac"`
	Interface string `json:"interface"` // "Ethernet" | "802.11" (WiFi)
	Active    bool   `json:"active"`
}

// Client is a stateful Sagemcom F@st JSON-API client. Not safe for concurrent
// use; callers should serialize (the status layer caches results).
type Client struct {
	baseURL      string
	username     string
	passwordHash string
	method       string // "sha512" or "md5"
	httpc        *http.Client

	serverNonce  string
	sessionID    int
	requestID    int
	currentNonce int
}

// New builds a client. baseURL like "http://192.168.2.1". method is "sha512"
// for the Bell Giga Hub, "md5" for older Home Hub 2000/3000.
func New(baseURL, username, password, method string) *Client {
	method = strings.ToLower(strings.TrimSpace(method))
	if method != "md5" {
		method = "sha512"
	}
	return &Client{
		baseURL:      strings.TrimRight(baseURL, "/"),
		username:     username,
		passwordHash: hashHex(method, password),
		method:       method,
		httpc: &http.Client{
			Timeout: 15 * time.Second,
			// Local gateways ship self-signed certs; this only ever talks to
			// the LAN router address the operator configured.
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			},
		},
		requestID: -1,
	}
}

func hashHex(method, s string) string {
	if method == "md5" {
		sum := md5.Sum([]byte(s))
		return hex.EncodeToString(sum[:])
	}
	sum := sha512.Sum512([]byte(s))
	return hex.EncodeToString(sum[:])
}

func (c *Client) authKey() string {
	credential := hashHex(c.method,
		c.username+":"+c.serverNonce+":"+c.passwordHash)
	authString := fmt.Sprintf("%s:%d:%d:JSON:%s",
		credential, c.requestID, c.currentNonce, apiEndpoint)
	return hashHex(c.method, authString)
}

type apiReply struct {
	Reply struct {
		Error struct {
			Description string `json:"description"`
		} `json:"error"`
		Actions []struct {
			Error struct {
				Description string `json:"description"`
			} `json:"error"`
			Callbacks []struct {
				Parameters json.RawMessage `json:"parameters"`
			} `json:"callbacks"`
		} `json:"actions"`
	} `json:"reply"`
}

// request builds the signed envelope, posts it, and returns the parsed reply.
func (c *Client) request(actions []map[string]any, priority bool) (*apiReply, error) {
	c.requestID++
	c.currentNonce = rand.Intn(500000)

	payload := map[string]any{
		"request": map[string]any{
			"id":         c.requestID,
			"session-id": c.sessionID,
			"priority":   priority,
			"actions":    actions,
			"cnonce":     c.currentNonce,
			"auth-key":   c.authKey(),
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	form := url.Values{}
	form.Set("req", string(body))
	req, err := http.NewRequest(http.MethodPost, c.baseURL+apiEndpoint,
		bytes.NewBufferString(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "jarvis-home-assist")

	resp, err := c.httpc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("router http %d", resp.StatusCode)
	}

	var reply apiReply
	if err := json.NewDecoder(resp.Body).Decode(&reply); err != nil {
		return nil, err
	}
	if d := reply.Reply.Error.Description; d != "" && d != "XMO_REQUEST_NO_ERR" && d != "Ok" {
		return &reply, fmt.Errorf("router error: %s", d)
	}
	for _, a := range reply.Reply.Actions {
		if d := a.Error.Description; d != "" && d != "XMO_NO_ERR" {
			return &reply, fmt.Errorf("router action error: %s", d)
		}
	}
	return &reply, nil
}

// login authenticates and captures the session id + server nonce.
func (c *Client) login() error {
	c.serverNonce = ""
	c.sessionID = 0
	c.requestID = -1

	actions := []map[string]any{{
		"id":     0,
		"method": "logIn",
		"parameters": map[string]any{
			"user":       c.username,
			"persistent": true,
			"session-options": map[string]any{
				"nss":              []map[string]any{{"name": "gtw", "uri": "http://sagemcom.com/gateway-data"}},
				"language":         "ident",
				"context-flags":    map[string]any{"get-content-name": true, "local-time": true},
				"capability-depth": 2,
				"capability-flags": map[string]any{
					"name": true, "default-value": false,
					"restriction": true, "description": false,
				},
				"time-format": "ISO_8601",
			},
		},
	}}

	reply, err := c.request(actions, true)
	if err != nil {
		return err
	}
	params := firstParams(reply)
	if params == nil {
		return fmt.Errorf("login: no session in reply")
	}
	var s struct {
		ID    *int   `json:"id"`
		Nonce string `json:"nonce"`
	}
	if err := json.Unmarshal(params, &s); err != nil {
		return err
	}
	if s.ID == nil || s.Nonce == "" {
		return fmt.Errorf("login: unauthorized (check password / encryption method)")
	}
	c.sessionID = *s.ID
	c.serverNonce = s.Nonce
	return nil
}

func firstParams(reply *apiReply) json.RawMessage {
	if len(reply.Reply.Actions) == 0 {
		return nil
	}
	cbs := reply.Reply.Actions[0].Callbacks
	if len(cbs) == 0 {
		return nil
	}
	return cbs[0].Parameters
}

// Hosts logs in, fetches the connected-device list, and logs out.
func (c *Client) Hosts() ([]Host, error) {
	if err := c.login(); err != nil {
		return nil, err
	}
	defer c.logout()

	actions := []map[string]any{{
		"id":     0,
		"method": "getValue",
		"xpath":  "Device/Hosts/Hosts",
		"options": map[string]any{
			"capability-flags": map[string]any{"interface": true},
		},
	}}
	reply, err := c.request(actions, false)
	if err != nil {
		return nil, err
	}
	params := firstParams(reply)
	if params == nil {
		return nil, fmt.Errorf("hosts: empty reply")
	}
	var wrap struct {
		Value []struct {
			UID           int    `json:"Uid"`
			PhysAddress   string `json:"PhysAddress"`
			IPAddress     string `json:"IPAddress"`
			HostName      string `json:"HostName"`
			UserHostName  string `json:"UserHostName"`
			Active        bool   `json:"Active"`
			InterfaceType string `json:"InterfaceType"`
		} `json:"value"`
	}
	if err := json.Unmarshal(params, &wrap); err != nil {
		return nil, err
	}

	out := make([]Host, 0, len(wrap.Value))
	for _, h := range wrap.Value {
		if h.PhysAddress == "" {
			continue
		}
		name := h.UserHostName
		if name == "" {
			name = h.HostName
		}
		out = append(out, Host{
			UID:       h.UID,
			Name:      name,
			IP:        h.IPAddress,
			MAC:       strings.ToLower(h.PhysAddress),
			Interface: h.InterfaceType,
			Active:    h.Active,
		})
	}
	return out, nil
}

// SetValue writes a single value at an xpath (mirrors the library setValue).
// Used for device blocking; the exact xpath is firmware-specific and supplied
// by config, so this never writes a hardcoded/guessed path.
func (c *Client) SetValue(xpath, value string) error {
	if err := c.login(); err != nil {
		return err
	}
	defer c.logout()
	actions := []map[string]any{{
		"id":         0,
		"method":     "setValue",
		"xpath":      xpath,
		"parameters": map[string]any{"value": value},
		"options":    map[string]any{},
	}}
	_, err := c.request(actions, false)
	return err
}

// BlockDevice blocks/unblocks a device by MAC using a configured xpath
// template. The template may contain {mac} and/or {uid} placeholders; {uid}
// triggers a host lookup to resolve the router's internal id. Returns a clear
// error when blocking isn't configured — nothing is written in that case.
func (c *Client) BlockDevice(mac string, block bool, xpathTmpl, onVal, offVal string) error {
	if strings.TrimSpace(xpathTmpl) == "" {
		return errors.New("router blocking not configured (set ROUTER_BLOCK_XPATH)")
	}
	xpath := strings.ReplaceAll(xpathTmpl, "{mac}", mac)
	if strings.Contains(xpath, "{uid}") {
		hosts, err := c.Hosts()
		if err != nil {
			return err
		}
		uid := ""
		for _, h := range hosts {
			if strings.EqualFold(h.MAC, mac) {
				uid = fmt.Sprintf("%d", h.UID)
				break
			}
		}
		if uid == "" {
			return fmt.Errorf("device %s not found on router", mac)
		}
		xpath = strings.ReplaceAll(xpath, "{uid}", uid)
	}
	val := offVal
	if block {
		val = onVal
	}
	return c.SetValue(xpath, val)
}

func (c *Client) logout() {
	_, _ = c.request([]map[string]any{{"id": 0, "method": "logOut"}}, false)
	c.sessionID = -1
	c.serverNonce = ""
	c.requestID = -1
}
