package handlers

import (
	"encoding/json"
	"testing"
)

// The LAN device list is serialized into the status payload. On Windows the
// ARP branch is never taken, so this used to hand back a nil slice — JSON
// `null` instead of `[]`. Callers had to guard it individually; now the source
// guarantees a list.
func TestReadLANDevicesNeverNil(t *testing.T) {
	// No router configured: falls through to the ARP path.
	saved := RouterProvider
	RouterProvider = nil
	t.Cleanup(func() { RouterProvider = saved })

	devs := readLANDevices()
	if devs == nil {
		t.Fatal("readLANDevices returned nil")
	}
	if raw, _ := json.Marshal(devs); string(raw) == "null" {
		t.Errorf("marshalled to null, want []")
	}

	// A router that reports nothing must also degrade to an empty list, not nil.
	RouterProvider = func() ([]LANDevice, error) { return nil, nil }
	if devs := readLANDevices(); devs == nil {
		t.Error("readLANDevices returned nil when the router reported no devices")
	}
}
