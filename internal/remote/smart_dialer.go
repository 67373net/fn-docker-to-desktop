package remote

import (
	"context"
	"net"
	"sync"
	"time"
)

type stickyRoute struct {
	mode      string    // "direct" or "ssh"
	expiresAt time.Time // expiration timestamp
}

// SmartDialer implements smart fallback dialing (Direct first -> Sticky cache -> SSH tunnel fallback).
type SmartDialer struct {
	storage      *Storage
	sshMgr       *SSHManager
	mu           sync.RWMutex
	stickyRoutes map[string]stickyRoute // key: targetAddr e.g. "1.2.3.4:8080"
}

// NewSmartDialer creates a new SmartDialer.
func NewSmartDialer(storage *Storage, sshMgr *SSHManager) *SmartDialer {
	return &SmartDialer{
		storage:      storage,
		sshMgr:       sshMgr,
		stickyRoutes: make(map[string]stickyRoute),
	}
}

// DialContext handles smart dialed connections.
func (d *SmartDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	hostPart, _, err := net.SplitHostPort(addr)
	if err != nil {
		hostPart = addr
	}

	// 1. Check if the destination host is one of our configured remote hosts
	h, hasHost := d.storage.GetHostByAddress(hostPart)

	// If not a managed host or host has no SSH credentials, use standard direct dial
	if !hasHost || (h.Password == "" && h.PrivateKey == "") {
		return (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext(ctx, network, addr)
	}

	// 2. Check sticky route cache
	d.mu.RLock()
	route, hasRoute := d.stickyRoutes[addr]
	d.mu.RUnlock()

	now := time.Now()
	if hasRoute && now.Before(route.expiresAt) {
		if route.mode == "ssh" {
			// Fast path: known blocked/internal port, dial via SSH tunnel directly
			conn, sshErr := d.sshMgr.Dial(h, addr)
			if sshErr == nil {
				return conn, nil
			}
			// If SSH failed, invalidate sticky cache and continue to retry
			d.mu.Lock()
			delete(d.stickyRoutes, addr)
			d.mu.Unlock()
		} else if route.mode == "direct" {
			// Fast path: known accessible directly
			conn, dirErr := (&net.Dialer{
				Timeout:   3 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext(ctx, network, addr)
			if dirErr == nil {
				return conn, nil
			}
			// If direct failed, invalidate sticky cache and continue to retry
			d.mu.Lock()
			delete(d.stickyRoutes, addr)
			d.mu.Unlock()
		}
	}

	// 3. Step 1: Prioritize fast direct connection (1200ms short timeout)
	quickDialer := &net.Dialer{
		Timeout:   1200 * time.Millisecond,
		KeepAlive: 30 * time.Second,
	}
	conn, directErr := quickDialer.DialContext(ctx, network, addr)
	if directErr == nil {
		// Direct succeeded: mark direct in sticky cache for 5 minutes
		d.mu.Lock()
		d.stickyRoutes[addr] = stickyRoute{
			mode:      "direct",
			expiresAt: now.Add(5 * time.Minute),
		}
		d.mu.Unlock()
		return conn, nil
	}

	// 4. Step 2: Fallback to SSH tunnel piercing
	conn, sshErr := d.sshMgr.Dial(h, addr)
	if sshErr == nil {
		// SSH tunnel succeeded: mark ssh in sticky cache for 10 minutes
		d.mu.Lock()
		d.stickyRoutes[addr] = stickyRoute{
			mode:      "ssh",
			expiresAt: now.Add(10 * time.Minute),
		}
		d.mu.Unlock()
		return conn, nil
	}

	// Both failed, return the primary direct error (or SSH error)
	return nil, directErr
}
