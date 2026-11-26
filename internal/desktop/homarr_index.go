package desktop

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// HomarrIconMeta represents an entry in homarr-labs/dashboard-icons metadata.json.
type HomarrIconMeta struct {
	Base       string   `json:"base"`
	Aliases    []string `json:"aliases"`
	Categories []string `json:"categories"`
}

// HomarrSearchResult represents a verified matched icon from the Homarr index.
type HomarrSearchResult struct {
	ID    string
	Title string
	URL   string
	Score int
	Exact bool
}

var (
	homarrIndexMu      sync.RWMutex
	homarrIndexFetchMu sync.Mutex
	homarrIndexCache   map[string]HomarrIconMeta
	homarrIndexExpiry  time.Time
)

var homarrMetadataURLs = []string{
	"https://fastly.jsdelivr.net/gh/homarr-labs/dashboard-icons/metadata.json",
	"https://cdn.jsdelivr.net/gh/homarr-labs/dashboard-icons/metadata.json",
	"https://raw.githubusercontent.com/homarr-labs/dashboard-icons/main/metadata.json",
}

// SetHomarrIndexForTest overrides the in-memory Homarr metadata cache for deterministic unit tests.
func SetHomarrIndexForTest(index map[string]HomarrIconMeta, ttl time.Duration) {
	homarrIndexMu.Lock()
	defer homarrIndexMu.Unlock()
	homarrIndexCache = index
	homarrIndexExpiry = time.Now().Add(ttl)
}

func getHomarrIndex(ctx context.Context) (map[string]HomarrIconMeta, error) {
	homarrIndexMu.RLock()
	if homarrIndexCache != nil && time.Now().Before(homarrIndexExpiry) {
		idx := homarrIndexCache
		homarrIndexMu.RUnlock()
		return idx, nil
	}
	homarrIndexMu.RUnlock()

	homarrIndexFetchMu.Lock()
	defer homarrIndexFetchMu.Unlock()

	// Double-check after acquiring fetch lock
	homarrIndexMu.RLock()
	if homarrIndexCache != nil && time.Now().Before(homarrIndexExpiry) {
		idx := homarrIndexCache
		homarrIndexMu.RUnlock()
		return idx, nil
	}
	homarrIndexMu.RUnlock()

	client := &http.Client{Timeout: 6 * time.Second}
	var lastErr error
	for _, endpoint := range homarrMetadataURLs {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			lastErr = err
			continue
		}
		req.Header.Set("User-Agent", "fn-docker-to-desktop/1.0")
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}

		var parsed map[string]HomarrIconMeta
		decErr := json.NewDecoder(resp.Body).Decode(&parsed)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("HTTP %d from %s", resp.StatusCode, endpoint)
			continue
		}
		if decErr != nil {
			lastErr = decErr
			continue
		}

		if len(parsed) > 0 {
			homarrIndexMu.Lock()
			homarrIndexCache = parsed
			homarrIndexExpiry = time.Now().Add(12 * time.Hour)
			homarrIndexMu.Unlock()
			slog.Info("已拉取并缓存 Homarr 官方图标索引", "count", len(parsed), "source", endpoint)
			return parsed, nil
		}
	}

	// Return stale cache if available on transient failure
	homarrIndexMu.RLock()
	stale := homarrIndexCache
	homarrIndexMu.RUnlock()
	if stale != nil {
		return stale, nil
	}
	return nil, lastErr
}

// extractSearchTokens builds clean, high-signal search tokens from a display name and docker image.
// Unlike GetIconCandidates, it avoids emitting short generic fragments (e.g., "my", "app", "v2")
// that cause unrelated false-positive matches.
func extractSearchTokens(name, image string) []string {
	var tokens []string
	seen := make(map[string]bool)

	addToken := func(s string) {
		s = strings.TrimSpace(strings.ToLower(s))
		clean := strings.Map(func(r rune) rune {
			if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
				return r
			}
			if r == '_' || r == ' ' || r == '.' {
				return '-'
			}
			return -1
		}, s)
		for strings.Contains(clean, "--") {
			clean = strings.ReplaceAll(clean, "--", "-")
		}
		clean = strings.Trim(clean, "-")
		if clean == "" || seen[clean] {
			return
		}
		seen[clean] = true
		tokens = append(tokens, clean)
	}

	// 1. Docker image repository name (highest signal, e.g., "linuxserver/qbittorrent:latest" -> "qbittorrent")
	if image = strings.TrimSpace(image); image != "" {
		imgPart := image
		if strings.Contains(imgPart, "/") {
			parts := strings.Split(imgPart, "/")
			imgPart = parts[len(parts)-1]
		}
		if strings.Contains(imgPart, ":") {
			imgPart = strings.Split(imgPart, ":")[0]
		}
		if strings.Contains(imgPart, "@") {
			imgPart = strings.Split(imgPart, "@")[0]
		}
		addToken(imgPart)
		// Strip common edition suffixes like -ce, -ee, -oss, -server, -webui
		for _, suffix := range []string{"-ce", "-ee", "-oss", "-server", "-webui", "-docker"} {
			if strings.HasSuffix(strings.ToLower(imgPart), suffix) && len(imgPart) > len(suffix)+2 {
				addToken(imgPart[:len(imgPart)-len(suffix)])
			}
		}
	}

	// 2. Display name
	if name = strings.TrimSpace(name); name != "" {
		addToken(name)
		// Extract continuous alphanumeric words >= 3 chars from mixed Chinese/English titles
		for _, word := range strings.FieldsFunc(name, func(r rune) bool {
			return !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_')
		}) {
			if len(word) >= 3 {
				addToken(word)
			}
		}
		// Specific high-precision Chinese brand/service mappings (no broad generic words)
		brandAliasMap := map[string][]string{
			"百度网盘": {"baidu", "baidupan"},
			"百度盘":  {"baidu", "baidupan"},
			"阿里云盘": {"alipan", "aliyun"},
			"迅雷":   {"xunlei", "thunder"},
			"夸克":   {"quark"},
			"青龙":   {"qinglong"},
			"小雅":   {"xiaoya", "alist"},
			"思源":   {"siyuan"},
			"爱快":   {"ikuai"},
			"飞牛":   {"fnos"},
			"宝塔":   {"baota"},
			"哪吒":   {"nezha"},
			"微力同步": {"verysync"},
			"易有云":  {"linkease"},
			"极空间":  {"zspace"},
			"群晖":   {"synology"},
			"威联通":  {"qnap"},
			"微信":   {"wechat"},
			"哔哩哔哩": {"bilibili"},
			"网易云":  {"netease"},
		}
		for k, list := range brandAliasMap {
			if strings.Contains(name, k) {
				for _, item := range list {
					addToken(item)
				}
			}
		}
	}

	return tokens
}

// SearchHomarrIcons queries the cached Homarr dashboard-icons metadata index and returns
// verified, relevance-ranked icon suggestions.
func SearchHomarrIcons(ctx context.Context, name, image string, limit int) []HomarrSearchResult {
	if limit <= 0 {
		limit = 12
	}
	tokens := extractSearchTokens(name, image)
	if len(tokens) == 0 {
		return nil
	}

	index, err := getHomarrIndex(ctx)
	if err != nil || len(index) == 0 {
		slog.Debug("Homarr 图标索引不可用", "error", err)
		return nil
	}

	var results []HomarrSearchResult
	for id, meta := range index {
		idLower := strings.ToLower(id)
		bestScore := 0
		exact := false

		for tokIdx, tok := range tokens {
			// Earlier tokens (image name / full name) get slight priority tie-breaker
			tokBonus := 0
			if tokIdx == 0 {
				tokBonus = 5
			}

			score := 0
			if idLower == tok {
				score = 100 + tokBonus
				exact = true
			} else {
				// Check aliases
				aliasExact := false
				aliasContains := false
				for _, al := range meta.Aliases {
					alNorm := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(al), " ", "-"))
					if alNorm == tok {
						aliasExact = true
						break
					}
					if len(tok) >= 4 && strings.Contains(alNorm, tok) {
						aliasContains = true
					}
				}
				if aliasExact {
					score = 90 + tokBonus
					exact = true
				} else if len(idLower) >= 3 && len(tok) >= 3 && (strings.HasPrefix(idLower, tok+"-") || strings.HasPrefix(tok, idLower+"-")) {
					score = 72 + tokBonus
				} else if len(tok) >= 4 && strings.Contains(idLower, tok) {
					// Prefer shorter/closer IDs
					lenPenalty := len(idLower) - len(tok)
					if lenPenalty > 20 {
						lenPenalty = 20
					}
					score = 55 - lenPenalty + tokBonus
				} else if aliasContains {
					score = 45 + tokBonus
				}
			}

			if score > bestScore {
				bestScore = score
			}
		}

		if bestScore > 0 {
			results = append(results, HomarrSearchResult{
				ID:    id,
				Title: id,
				URL:   fmt.Sprintf("https://fastly.jsdelivr.net/gh/homarr-labs/dashboard-icons/png/%s.png", id),
				Score: bestScore,
				Exact: exact,
			})
		}
	}

	sort.Slice(results, func(i, j int) bool {
		if results[i].Score != results[j].Score {
			return results[i].Score > results[j].Score
		}
		if len(results[i].ID) != len(results[j].ID) {
			return len(results[i].ID) < len(results[j].ID)
		}
		return results[i].ID < results[j].ID
	})

	if len(results) > limit {
		results = results[:limit]
	}
	return results
}
