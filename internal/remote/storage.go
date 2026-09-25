package remote

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Storage handles persistent storage of remote hosts in hosts.json.
type Storage struct {
	mu       sync.RWMutex
	filePath string
	hosts    map[string]HostConfig
}

// NewStorage creates a new Host Storage.
func NewStorage(dataDir string) (*Storage, error) {
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create data dir: %w", err)
	}

	s := &Storage{
		filePath: filepath.Join(dataDir, "hosts.json"),
		hosts:    make(map[string]HostConfig),
	}

	if err := s.load(); err != nil {
		return nil, err
	}

	return s, nil
}

func (s *Storage) load() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := os.ReadFile(s.filePath)
	if err != nil {
		if os.IsNotExist(err) {
			s.hosts = make(map[string]HostConfig)
			return nil
		}
		return fmt.Errorf("failed to read hosts file: %w", err)
	}

	var list []HostConfig
	if err := json.Unmarshal(data, &list); err != nil {
		return fmt.Errorf("failed to parse hosts file: %w", err)
	}

	s.hosts = make(map[string]HostConfig, len(list))
	for _, h := range list {
		s.hosts[h.ID] = h
	}

	return nil
}

func (s *Storage) saveLocked() error {
	list := make([]HostConfig, 0, len(s.hosts))
	for _, h := range s.hosts {
		list = append(list, h)
	}

	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal hosts: %w", err)
	}

	tmpFile := s.filePath + ".tmp"
	if err := os.WriteFile(tmpFile, data, 0600); err != nil {
		return fmt.Errorf("failed to write tmp hosts file: %w", err)
	}

	if err := os.Rename(tmpFile, s.filePath); err != nil {
		return fmt.Errorf("failed to replace hosts file: %w", err)
	}

	return nil
}

func generateHostID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return "host-" + hex.EncodeToString(b)
}

// GetAllHosts returns all configured hosts.
func (s *Storage) GetAllHosts() []HostConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()

	list := make([]HostConfig, 0, len(s.hosts))
	for _, h := range s.hosts {
		list = append(list, h)
	}
	return list
}

// GetHost returns a host by ID.
func (s *Storage) GetHost(id string) (HostConfig, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	h, ok := s.hosts[id]
	return h, ok
}

// GetHostByAddress looks up a host by IP or domain.
func (s *Storage) GetHostByAddress(addr string) (HostConfig, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, h := range s.hosts {
		if h.Host == addr {
			return h, true
		}
	}
	return HostConfig{}, false
}

// CleanHostAddress normalizes a host address by removing lan: prefix, protocol schemes, paths, and trailing ports.
func CleanHostAddress(addr string) string {
	a := strings.TrimSpace(addr)
	a = strings.TrimPrefix(a, "lan:")
	a = strings.TrimPrefix(a, "http://")
	a = strings.TrimPrefix(a, "https://")
	if idx := strings.Index(a, "/"); idx != -1 {
		a = a[:idx]
	}
	if host, _, err := net.SplitHostPort(a); err == nil {
		a = host
	}
	return strings.ToLower(strings.TrimSpace(a))
}

// SaveHost creates or updates a host.
func (s *Storage) SaveHost(h HostConfig) (HostConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	if h.ID == "" {
		cleanAddr := CleanHostAddress(h.Host)
		for _, existing := range s.hosts {
			if existing.Host == h.Host || CleanHostAddress(existing.Host) == cleanAddr {
				h.ID = existing.ID
				h.CreatedAt = existing.CreatedAt
				break
			}
		}
		if h.ID == "" {
			h.ID = generateHostID()
			h.CreatedAt = now
		}
	}
	h.UpdatedAt = now

	if h.SSHPort <= 0 {
		h.SSHPort = 22
	}
	if h.User == "" {
		h.User = "root"
	}
	if h.AuthType == "" {
		h.AuthType = "password"
	}
	if h.Status == "" {
		if h.Password == "" && h.PrivateKey == "" {
			h.Status = "unconfigured"
		} else {
			h.Status = "untested"
		}
	}

	s.hosts[h.ID] = h
	if err := s.saveLocked(); err != nil {
		return HostConfig{}, err
	}

	return h, nil
}

// UpdateStatus updates the connection status of a host.
func (s *Storage) UpdateStatus(id string, status string, errMsg string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	h, ok := s.hosts[id]
	if !ok {
		return
	}
	h.Status = status
	h.Error = errMsg
	h.UpdatedAt = time.Now()
	s.hosts[id] = h
	_ = s.saveLocked()
}

// DeleteHost removes a host by ID or host address. If host does not exist, returns nil (idempotent).
func (s *Storage) DeleteHost(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	cleanID := CleanHostAddress(id)

	deleted := false
	for k, h := range s.hosts {
		if k == id || h.ID == id || h.ID == cleanID || h.Host == id || CleanHostAddress(h.Host) == cleanID {
			delete(s.hosts, k)
			deleted = true
		}
	}

	if deleted {
		return s.saveLocked()
	}
	return nil
}
