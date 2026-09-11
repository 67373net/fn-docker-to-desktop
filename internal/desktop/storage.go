package desktop

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Storage manages persistent storage for desktop items and app settings.
type Storage struct {
	mu           sync.RWMutex
	dataDir      string
	itemsFile    string
	settingsFile string
	items        map[string]DesktopItem
	settings     Settings
}

// NewStorage initializes storage using dataDir.
func NewStorage(dataDir string) (*Storage, error) {
	if dataDir == "" {
		dataDir = "data"
	}
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, fmt.Errorf("无法创建数据目录 %s: %w", dataDir, err)
	}

	s := &Storage{
		dataDir:      dataDir,
		itemsFile:    filepath.Join(dataDir, "desktop_items.json"),
		settingsFile: filepath.Join(dataDir, "settings.json"),
		items:        make(map[string]DesktopItem),
		settings:     DefaultSettings(),
	}

	s.loadSettings()
	s.loadItems()
	return s, nil
}

func (s *Storage) loadSettings() {
	data, err := os.ReadFile(s.settingsFile)
	if err == nil {
		var loaded Settings
		if err := json.Unmarshal(data, &loaded); err == nil {
			if loaded.PortalPort <= 0 {
				loaded.PortalPort = 5900
			}
			if loaded.PortalName == "" || loaded.PortalName == "把Docker放到桌面" {
				loaded.PortalName = "把 Docker 放到桌面"
			}
			if loaded.PortalUIType == "" {
				loaded.PortalUIType = "iframe"
			}
			s.settings = loaded
			return
		}
	}
	// Save default if file didn't exist
	_ = s.saveSettingsLocked()
}

func (s *Storage) loadItems() {
	data, err := os.ReadFile(s.itemsFile)
	if err == nil {
		var list []DesktopItem
		if err := json.Unmarshal(data, &list); err == nil {
			now := time.Now()
			for idx, item := range list {
				// If legacy item has zero CreatedAt, assign deterministic decreasing time based on file order
				if item.CreatedAt.IsZero() {
					item.CreatedAt = now.Add(-time.Duration(idx) * time.Minute)
				}
				s.items[item.ID] = item
			}
		}
	}
}

func (s *Storage) saveSettingsLocked() error {
	data, err := json.MarshalIndent(s.settings, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.settingsFile, data, 0644)
}

func (s *Storage) saveItemsLocked() error {
	list := s.getAllItemsLocked()
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.itemsFile, data, 0644)
}

// GetSettings returns current application settings.
func (s *Storage) GetSettings() Settings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.settings
}

// UpdateSettings updates application settings.
func (s *Storage) UpdateSettings(settings Settings) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if settings.PortalPort <= 0 {
		settings.PortalPort = 5900
	}
	if settings.PortalName == "" || settings.PortalName == "把Docker放到桌面" {
		settings.PortalName = "把 Docker 放到桌面"
	}
	if settings.PortalUIType == "" {
		settings.PortalUIType = "iframe"
	}
	s.settings = settings
	return s.saveSettingsLocked()
}

// GetAllItems returns a list of all desktop items stably sorted by CreatedAt descending (newest first).
func (s *Storage) GetAllItems() []DesktopItem {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.getAllItemsLocked()
}

func (s *Storage) getAllItemsLocked() []DesktopItem {
	list := make([]DesktopItem, 0, len(s.items))
	for _, item := range s.items {
		list = append(list, item)
	}
	sort.Slice(list, func(i, j int) bool {
		if !list[i].CreatedAt.Equal(list[j].CreatedAt) {
			return list[i].CreatedAt.After(list[j].CreatedAt)
		}
		return list[i].ID > list[j].ID
	})
	return list
}

// GetItem returns a single desktop item by ID.
func (s *Storage) GetItem(id string) (DesktopItem, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	item, ok := s.items[id]
	return item, ok
}

// GetItemByAppName finds a desktop item by its fnOS AppName.
func (s *Storage) GetItemByAppName(appName string) (DesktopItem, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, it := range s.items {
		if it.AppName == appName {
			return it, true
		}
	}
	return DesktopItem{}, false
}

// SaveItem saves or updates a desktop item.
func (s *Storage) SaveItem(item DesktopItem) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	if existing, ok := s.items[item.ID]; ok {
		item.CreatedAt = existing.CreatedAt
		item.UpdatedAt = now
	} else {
		item.CreatedAt = now
		item.UpdatedAt = now
	}

	s.items[item.ID] = item
	return s.saveItemsLocked()
}

// DeleteItem removes a desktop item by ID.
func (s *Storage) DeleteItem(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.items, id)
	return s.saveItemsLocked()
}
