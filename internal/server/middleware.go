package server

import (
	"crypto/subtle"
	"net/http"
	"net/url"
	"strings"

	"github.com/d-kuro/kirocc/internal/httpx"
	"github.com/d-kuro/kirocc/internal/logging"
	"github.com/d-kuro/kirocc/internal/tracing"
)

func traceMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		traceID := tracing.ExtractTraceID(r.Context())
		if traceID == "" {
			traceID = logging.NewTraceID()
		}
		ctx := logging.WithTraceID(r.Context(), traceID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// authMiddleware enforces API key authentication when configured.
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.apiKey == "" {
			next.ServeHTTP(w, r)
			return
		}
		if r.URL.Path == "/health" {
			next.ServeHTTP(w, r)
			return
		}
		// Accept "Authorization: Bearer <key>" (Claude Code) or
		// "x-api-key: <key>" (the header the Anthropic SDKs send).
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok {
			token = r.Header.Get("X-Api-Key")
			ok = token != ""
		}
		if !ok || subtle.ConstantTimeCompare([]byte(token), []byte(s.apiKey)) != 1 {
			httpx.WriteError(w, http.StatusUnauthorized, httpx.ErrTypeAuthentication, "invalid API key")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// corsAllowHeaders lists the request headers browser-based clients may send:
// the auth headers, plus the headers the Anthropic SDK requires on direct
// browser requests (anthropic-version, anthropic-dangerous-direct-browser-access).
const corsAllowHeaders = "Content-Type, Authorization, X-Api-Key, Anthropic-Version, Anthropic-Dangerous-Direct-Browser-Access"

// corsMiddleware adds CORS headers for localhost origins.
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if isLocalhostOrigin(origin) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", corsAllowHeaders)
			w.Header().Set("Vary", "Origin")
		}

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// isLocalhostOrigin checks if the origin is a localhost URL using strict URL parsing.
func isLocalhostOrigin(origin string) bool {
	if origin == "" {
		return false
	}
	u, err := url.Parse(origin)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return false
	}
	// Reject origins with userinfo or path components that could be spoofed.
	if u.User != nil || (u.Path != "" && u.Path != "/") {
		return false
	}
	host := u.Hostname()
	// tauri.localhost is the webview origin of Tauri apps on Windows
	// (tauri://localhost on macOS and Linux is already covered).
	return host == "localhost" || host == "127.0.0.1" || host == "::1" || host == "tauri.localhost"
}
