package desktop

import (
	"context"
	"testing"
	"time"
)

func TestSearchHomarrIconsRelevance(t *testing.T) {
	SetHomarrIndexForTest(map[string]HomarrIconMeta{
		"qbittorrent":   {Base: "svg", Aliases: []string{"qbit"}},
		"qbittorrent-nox": {Base: "svg"},
		"app":           {Base: "svg"},
		"my":            {Base: "svg"},
		"nextcloud":     {Base: "svg"},
		"portainer":     {Base: "svg", Aliases: []string{"portainer-ce"}},
		"home-assistant": {Base: "svg", Aliases: []string{"hass"}},
	}, 10*time.Minute)

	ctx := context.Background()

	// 1. Generic Chinese title containing "我的文件" should NOT match unrelated "nextcloud" or "my"
	res := SearchHomarrIcons(ctx, "我的文件工具", "", 10)
	if len(res) != 0 {
		t.Fatalf("expected no false-positive matches for '我的文件工具', got %+v", res)
	}

	// 2. "my-custom-app" should NOT match "my" or "app" (2-3 char fragment traps)
	res = SearchHomarrIcons(ctx, "my-custom-app", "", 10)
	for _, r := range res {
		if r.ID == "my" {
			t.Fatalf("unexpected false-positive fragment match 'my' for 'my-custom-app'")
		}
	}

	// 3. Docker image linuxserver/qbittorrent:latest should rank qbittorrent #1 as Exact
	res = SearchHomarrIcons(ctx, "下载器", "linuxserver/qbittorrent:latest", 10)
	if len(res) == 0 || res[0].ID != "qbittorrent" || !res[0].Exact {
		t.Fatalf("expected qbittorrent exact match #1, got %+v", res)
	}

	// 4. Alias match: "hass" -> "home-assistant"
	res = SearchHomarrIcons(ctx, "hass", "", 10)
	if len(res) == 0 || res[0].ID != "home-assistant" || !res[0].Exact {
		t.Fatalf("expected home-assistant exact alias match, got %+v", res)
	}
}
