package desktop

import "time"

// ItemMode represents the mode of a desktop item.
type ItemMode string

const (
	ModeLocalPort ItemMode = "local"    // Existing host port
	ModeProxy     ItemMode = "proxy"    // Reverse proxy LAN/WAN service to local port
	ModeShortcut  ItemMode = "shortcut" // Pure URL shortcut
)

// DesktopItem represents a service or shortcut pinned to fnOS desktop.
type DesktopItem struct {
	ID            string    `json:"id"`                       // Unique ID (slug/alphanumeric)
	Name          string    `json:"name"`                     // Display title on desktop
	Desc          string    `json:"desc"`                     // Description
	Mode          ItemMode  `json:"mode"`                     // "local", "proxy", "shortcut"
	AppName       string    `json:"app_name,omitempty"`       // fnOS package appname (e.g. fndocker.xxx)
	ContainerName string    `json:"container_name,omitempty"` // Associated container name (if any)
	Image         string    `json:"image,omitempty"`          // Docker image name (e.g. linuxserver/qbittorrent)
	TargetURL     string    `json:"target_url,omitempty"`     // Target backend URL (for proxy / shortcut)
	Port          int       `json:"port"`                     // Local port (for local port or proxy mode)
	Protocol      string    `json:"protocol"`                 // "http" or "https" (default: "http")
	Path          string    `json:"path"`                     // Path (default: "/")
	UIType        string    `json:"ui_type"`                  // "url" (browser new tab) or "iframe" (fnOS window)
	AllUsers      bool      `json:"all_users"`                // true: all users, false: admin only
	Icon          string    `json:"icon"`                     // Icon filename or URL
	SkipTLSVerify bool      `json:"skip_tls_verify,omitempty"`// Skip TLS check for self-signed certs
	Enabled       bool      `json:"enabled"`                  // Is active
	Installed     bool      `json:"installed"`                // Is installed in fnOS App Center
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// Settings represents global configuration for fn-docker-to-desktop itself.
type Settings struct {
	PortalPort     int    `json:"portal_port"`      // Default 5900
	PortalName     string `json:"portal_name"`      // Default "把 Docker 放到桌面"
	PortalUIType   string `json:"portal_ui_type"`   // "url" or "iframe"
	PortalAllUsers bool   `json:"portal_all_users"` // true: all users, false: admin only
	PortalIcon     string `json:"portal_icon"`      // Icon path
	AuthPassword   string `json:"auth_password,omitempty"`
}

// DefaultSettings returns initial settings for the application.
func DefaultSettings() Settings {
	return Settings{
		PortalPort:     5900,
		PortalName:     "把 Docker 放到桌面",
		PortalUIType:   "iframe",
		PortalAllUsers: false, // Default admin only
		PortalIcon:     "icon.png",
		AuthPassword:   "",
	}
}
