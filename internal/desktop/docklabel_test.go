package desktop

import (
	"encoding/json"
	"os"
	"testing"
)

func TestScanDockLabelItemsHost(t *testing.T) {
	if _, err := os.Stat("/var/run/docker.sock"); err != nil {
		t.Skip("docker socket not available")
	}
	items, err := ScanDockLabelItems(nil)
	if err != nil {
		t.Fatalf("ScanDockLabelItems failed: %v", err)
	}
	t.Logf("Found %d docklabel items", len(items))
	for _, item := range items {
		data, _ := json.Marshal(item)
		t.Logf("Item: %s", string(data))
	}
}
