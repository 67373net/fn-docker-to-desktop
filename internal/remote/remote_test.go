package remote

import (
	"context"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"fn-docker-to-desktop/internal/monitor"
)

func TestHostStorage(t *testing.T) {
	tempDir := t.TempDir()
	store, err := NewStorage(tempDir)
	if err != nil {
		t.Fatalf("NewStorage failed: %v", err)
	}

	// 1. Initial list empty
	hosts := store.GetAllHosts()
	if len(hosts) != 0 {
		t.Fatalf("expected 0 hosts, got %d", len(hosts))
	}

	// 2. Save a host
	h1 := HostConfig{
		Name:     "Test Server",
		Host:     "192.168.1.100",
		SSHPort:  22,
		User:     "root",
		AuthType: "password",
		Password: "secret-password",
	}
	saved, err := store.SaveHost(h1)
	if err != nil {
		t.Fatalf("SaveHost failed: %v", err)
	}
	if saved.ID == "" {
		t.Fatalf("expected generated ID, got empty")
	}

	// 3. Get the host
	retrieved, exists := store.GetHost(saved.ID)
	if !exists {
		t.Fatalf("host %s not found", saved.ID)
	}
	if retrieved.Name != "Test Server" || retrieved.Host != "192.168.1.100" {
		t.Fatalf("unexpected host content: %+v", retrieved)
	}

	// 4. Update the host
	retrieved.Name = "Updated Server"
	if _, err := store.SaveHost(retrieved); err != nil {
		t.Fatalf("SaveHost update failed: %v", err)
	}

	// 5. Verify reload from disk
	store2, err := NewStorage(tempDir)
	if err != nil {
		t.Fatalf("reload NewStorage failed: %v", err)
	}
	reloaded, exists := store2.GetHost(saved.ID)
	if !exists {
		t.Fatalf("reloaded host %s not found", saved.ID)
	}
	if reloaded.Name != "Updated Server" {
		t.Fatalf("expected reloaded name 'Updated Server', got %q", reloaded.Name)
	}

	// 6. Delete host
	if err := store2.DeleteHost(saved.ID); err != nil {
		t.Fatalf("DeleteHost failed: %v", err)
	}
	if _, exists := store2.GetHost(saved.ID); exists {
		t.Fatalf("deleted host still exists")
	}
}

func TestParseRemoteCommandOutput(t *testing.T) {
	// Sample ss + docker ps output
	sampleOutput := `Netid State Recv-Q Send-Q Local Address:Port Peer Address:PortProcess
tcp LISTEN 0 128 0.0.0.0:80 0.0.0.0:* users:(("nginx",pid=1234,fd=6))
tcp LISTEN 0 128 0.0.0.0:8080 0.0.0.0:* users:(("docker-proxy",pid=5678,fd=4))
udp UNCONN 0 0 0.0.0.0:53 0.0.0.0:* users:(("dnsmasq",pid=890,fd=5))
---DOCKER_SEP---
{"ID":"abc12345","Names":"my-web-app","Image":"nginx:latest","Ports":"0.0.0.0:8080->80/tcp","Status":"Up 3 hours"}
`

	entries := parseRemoteCommandOutput(sampleOutput)
	if len(entries) == 0 {
		t.Fatalf("expected parsed entries, got 0")
	}

	var foundNginx, foundDockerApp, foundDNS bool
	for _, entry := range entries {
		if entry.LocalPort == 80 && entry.Protocol == "tcp" {
			foundNginx = true
			if entry.ProcessName != "nginx" || entry.PID != 1234 {
				t.Errorf("unexpected nginx info: %+v", entry)
			}
			if entry.Docker.IsDocker {
				t.Errorf("nginx port 80 should not be marked docker")
			}
		}
		if entry.LocalPort == 8080 && entry.Protocol == "tcp" {
			foundDockerApp = true
			if !entry.Docker.IsDocker || entry.Docker.ContainerName != "my-web-app" {
				t.Errorf("unexpected docker info: %+v", entry.Docker)
			}
		}
		if entry.LocalPort == 53 && entry.Protocol == "udp" {
			foundDNS = true
			if entry.ProcessName != "dnsmasq" {
				t.Errorf("unexpected udp proc: %+v", entry)
			}
		}
	}

	if !foundNginx {
		t.Errorf("nginx port 80 not parsed")
	}
	if !foundDockerApp {
		t.Errorf("docker port 8080 not parsed")
	}
	if !foundDNS {
		t.Errorf("dnsmasq port 53 not parsed")
	}
}

func TestSmartDialerState(t *testing.T) {
	tempDir := t.TempDir()
	store, _ := NewStorage(tempDir)
	store.SaveHost(HostConfig{
		Host:     "127.0.0.1",
		Password: "test",
	})
	sshMgr := NewSSHManager()

	sd := NewSmartDialer(store, sshMgr)

	// 1. Create a dummy TCP server
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen failed: %v", err)
	}
	addr := l.Addr().String()

	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()

	// 2. Set sticky direct route
	sd.mu.Lock()
	sd.stickyRoutes[addr] = stickyRoute{
		mode:      "direct",
		expiresAt: time.Now().Add(5 * time.Minute),
	}
	sd.mu.Unlock()

	sd.mu.RLock()
	route, ok := sd.stickyRoutes[addr]
	sd.mu.RUnlock()

	if !ok || route.mode != "direct" {
		t.Fatalf("expected direct mode in cache")
	}
	if time.Until(route.expiresAt) <= 0 {
		t.Fatalf("expected cache not expired")
	}

	// 3. Dial active listener -> should succeed
	ctx := context.Background()
	conn, err := sd.DialContext(ctx, "tcp", addr)
	if err != nil {
		t.Fatalf("expected dial to active listener to succeed: %v", err)
	}
	conn.Close()

	// 4. Close listener and dial -> should fail and invalidate sticky cache
	l.Close()
	time.Sleep(20 * time.Millisecond)

	failConn, failErr := sd.DialContext(ctx, "tcp", addr)
	if failErr == nil {
		failConn.Close()
	}
	sd.mu.RLock()
	_, stillExists := sd.stickyRoutes[addr]
	sd.mu.RUnlock()
	if stillExists {
		t.Fatalf("expected sticky route to be invalidated on connection failure")
	}
}

func TestParseARPTable(t *testing.T) {
	arpData := `IP address       HW type     Flags       HW address            Mask     Device
192.168.1.1      0x1         0x2         00:11:22:33:44:55     *        eth0
192.168.1.200    0x1         0x2         66:77:88:99:aa:bb     *        eth0
192.168.1.254    0x1         0x0         00:00:00:00:00:00     *        eth0
`
	hosts := parseARPReader(strings.NewReader(arpData))
	if len(hosts) != 2 {
		t.Fatalf("expected 2 active IPs (excluding incomplete 0x0 flag), got %d: %v", len(hosts), hosts)
	}
	if hosts[0].IP != "192.168.1.1" || hosts[1].IP != "192.168.1.200" {
		t.Fatalf("unexpected parsed IPs: %v", hosts)
	}
}

func TestProbeHostPorts(t *testing.T) {
	tempDir := t.TempDir()
	store, _ := NewStorage(tempDir)
	scanner := &LANScanner{storage: store}

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen failed: %v", err)
	}
	defer l.Close()

	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()

	_, portStr, _ := net.SplitHostPort(l.Addr().String())
	pNum, _ := strconv.Atoi(portStr)

	if empty := scanner.ProbeHostPorts(""); empty != nil {
		t.Errorf("expected nil for empty IP, got %+v", empty)
	}

	probed := scanner.ProbeHostPortsRange("127.0.0.1", pNum-5, pNum+5)
	var found bool
	for _, entry := range probed {
		if entry.LocalPort == pNum {
			found = true
			if !entry.NeedsSSH {
				t.Errorf("expected NeedsSSH to be true for probed port")
			}
			break
		}
	}
	if !found {
		t.Errorf("expected port %d to be detected by ProbeHostPortsRange", pNum)
	}
}

func TestLANScannerConfiguredAndUnmarked(t *testing.T) {
	tempDir := t.TempDir()
	store, _ := NewStorage(tempDir)
	scanner := &LANScanner{
		storage: store,
		cachedHosts: []DiscoveredLANHost{
			{IP: "192.168.1.55", MAC: "aa:bb:cc:dd:ee:ff", Status: "unconfigured"},
		},
	}

	// 1. Initial status: unconfigured
	st := scanner.GetStatus()
	if len(st.Hosts) != 1 || st.Hosts[0].Status != "unconfigured" {
		t.Fatalf("expected unconfigured host, got %+v", st.Hosts)
	}

	// 2. Configure the host in store
	saved, err := store.SaveHost(HostConfig{
		Host: "192.168.1.55",
		Name: "My NAS",
	})
	if err != nil {
		t.Fatalf("SaveHost failed: %v", err)
	}

	// 3. Status should now be configured
	st = scanner.GetStatus()
	if st.Hosts[0].Status != "configured" || st.Hosts[0].HostID != saved.ID || st.Hosts[0].Name != "My NAS" {
		t.Fatalf("expected configured host, got %+v", st.Hosts[0])
	}

	// 4. Delete the host from store
	if err := store.DeleteHost(saved.ID); err != nil {
		t.Fatalf("DeleteHost failed: %v", err)
	}
	scanner.UnmarkConfiguredHost("192.168.1.55")

	// 5. Status should immediately revert to unconfigured with cleared ID and Name
	st = scanner.GetStatus()
	if st.Hosts[0].Status != "unconfigured" || st.Hosts[0].HostID != "" || st.Hosts[0].Name != "" {
		t.Fatalf("expected unconfigured host after delete, got %+v", st.Hosts[0])
	}
}

func TestInFlightDeduplication(t *testing.T) {
	tempDir := t.TempDir()
	store, _ := NewStorage(tempDir)
	scanner := NewLANScanner(store)

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen failed: %v", err)
	}
	defer l.Close()

	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()

	_, portStr, _ := net.SplitHostPort(l.Addr().String())
	pNum, _ := strconv.Atoi(portStr)

	var wg sync.WaitGroup
	results := make([][]monitor.PortEntry, 3)
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			results[idx] = scanner.ProbeHostPorts("127.0.0.1")
		}(i)
	}
	wg.Wait()

	for i := 0; i < 3; i++ {
		var found bool
		for _, p := range results[i] {
			if p.LocalPort == pNum {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("worker %d did not find port %d in results", i, pNum)
		}
	}
}


