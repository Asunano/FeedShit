package middleware

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// ========== Proof of Work Verification ==========

func VerifyPoW(projectID, timestamp, nonce string, difficulty int) bool {
	// Validate timestamp first (cheap check before expensive hash)
	ts, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return false
	}
	serverTime := time.Now().Unix()
	diff := serverTime - ts
	if diff < 0 {
		diff = -diff
	}
	if diff > 300 {
		return false
	}

	// Now compute hash
	payload := projectID + timestamp + nonce
	hash := sha256.Sum256([]byte(payload))
	hashHex := hex.EncodeToString(hash[:])

	prefix := strings.Repeat("0", difficulty)
	if !strings.HasPrefix(hashHex, prefix) {
		return false
	}

	return true
}

// ========== Helpers ==========

func SecureCompare(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// GetClientIP extracts the real client IP address.
// CDN/proxy headers are only trusted when the direct connection is from a trusted proxy.
func GetClientIP(c *gin.Context) string {
	remoteIP, _, _ := net.SplitHostPort(c.Request.RemoteAddr)
	if remoteIP == "" {
		remoteIP = c.ClientIP()
	}

	// CDN provider mode:
	// "none" — never trust headers, use direct connection IP only
	// "auto" — try all known CDN/proxy headers in priority order
	// "cloudflare" — CF-Connecting-IP only
	// "generic" — X-Forwarded-For / X-Real-Ip / Forwarded
	if getCDNProvider() == "none" {
		return remoteIP
	}

	// Only read CDN/proxy headers when trusted proxies are explicitly configured
	// AND the direct connection is from a trusted proxy.
	if len(getTrustedProxies()) == 0 {
		return remoteIP
	}

	trusted := isTrustedProxy(remoteIP)
	if !trusted {
		return remoteIP
	}

	switch getCDNProvider() {
	case "cloudflare":
		// Cloudflare always sets CF-Connecting-IP, most trustworthy
		if cf := c.GetHeader("CF-Connecting-IP"); cf != "" {
			return cf
		}
	case "generic":
		// Generic proxy: X-Forwarded-For first, then X-Real-Ip
		if xff := c.GetHeader("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			if ip := strings.TrimSpace(parts[0]); ip != "" {
				return ip
			}
		}
		if xri := c.GetHeader("X-Real-Ip"); xri != "" {
			return xri
		}
	default: // "auto" — try all headers in priority order
		// CF-Connecting-IP: Cloudflare always sets this, most trustworthy
		if cf := c.GetHeader("CF-Connecting-IP"); cf != "" {
			return cf
		}
		// X-Forwarded-For: take the first (client) IP from the chain
		if xff := c.GetHeader("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			if ip := strings.TrimSpace(parts[0]); ip != "" {
				return ip
			}
		}
		// X-Real-Ip: commonly set by nginx
		if xri := c.GetHeader("X-Real-Ip"); xri != "" {
			return xri
		}
		// Forwarded: RFC 7239 standard header — parse "for=..." directive
		if fwd := c.GetHeader("Forwarded"); fwd != "" {
			for _, param := range strings.Split(fwd, ";") {
				param = strings.TrimSpace(param)
				if strings.HasPrefix(strings.ToLower(param), "for=") {
					forVal := param[4:]
					forVal = strings.Trim(forVal, `"[]`)
					if idx := strings.LastIndex(forVal, "]:"); idx > 0 {
						forVal = forVal[:idx+1]
					}
					forVal = strings.Trim(forVal, `"[]`)
					if forVal != "" {
						return forVal
					}
				}
			}
		}
	}
	return remoteIP
}

// trustedProxies and cdnProvider are protected by proxyMu to avoid data races
// between request-time reads (GetClientIP) and config-time writes
// (SetTrustedProxies / SetCDNProvider). Previously accessed as bare package
// globals, which trips `go test -race`.
var (
	proxyMu        sync.RWMutex
	trustedProxies []string
	trustedProxySet map[string]bool // O(1) lookup, built from trustedProxies
	cdnProvider    = "auto"
)

// SetTrustedProxies configures which proxy IPs are trusted for reading CDN headers.
func SetTrustedProxies(proxies []string) {
	proxyMu.Lock()
	defer proxyMu.Unlock()
	trustedProxies = proxies
	trustedProxySet = make(map[string]bool, len(proxies))
	for _, p := range proxies {
		trustedProxySet[p] = true
	}
}

func getTrustedProxies() []string {
	proxyMu.RLock()
	defer proxyMu.RUnlock()
	return trustedProxies
}

func isTrustedProxy(ip string) bool {
	proxyMu.RLock()
	defer proxyMu.RUnlock()
	if trustedProxySet["*"] {
		return true
	}
	return trustedProxySet[ip]
}

// SetCDNProvider sets the CDN provider for IP detection.
func SetCDNProvider(provider string) {
	proxyMu.Lock()
	defer proxyMu.Unlock()
	switch provider {
	case "none", "cloudflare", "generic", "auto":
		cdnProvider = provider
	default:
		cdnProvider = "auto"
	}
}

func getCDNProvider() string {
	proxyMu.RLock()
	defer proxyMu.RUnlock()
	return cdnProvider
}

// GetCDNProvider returns the current CDN provider setting.
func GetCDNProvider() string {
	return getCDNProvider()
}

// FormatSize converts bytes to a human-readable string.
func FormatSize(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	val := float64(bytes)
	units := []string{"KB", "MB", "GB", "TB"}
	for i, u := range units {
		val /= float64(unit)
		if val < float64(unit) || i == len(units)-1 {
			return fmt.Sprintf("%.1f %s", val, u)
		}
	}
	return fmt.Sprintf("%.1f %s", val, units[len(units)-1])
}
