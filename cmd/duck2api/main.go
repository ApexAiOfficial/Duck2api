package main

import (
	"aurora/internal/initialize"
	"encoding/json"
	"fmt"
	"log"
	"os"

	"github.com/gin-gonic/gin"

	"github.com/acheong08/endless"
)

// loadConfigFile reads config.json as defaults for environment variables,
// populating only keys that are not already set in the environment.
// Precedence: environment variables > config.json. Keep non-sensitive values
// in the file and override secrets (Authorization/PROXY_URL) via environment
// variables in the deployment settings.
// Configuration keys correspond directly to environment variable names;
// there is no additional naming scheme or section mapping.
// The path remains ./config.json; a -config flag can be added if needed.
func loadConfigFile(path string) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return // Missing configuration is normal in Docker/Koyeb deployments.
	}
	var kv map[string]any
	if err := json.Unmarshal(raw, &kv); err != nil {
		log.Fatalf("failed to parse %s: %v", path, err)
	}
	for key, val := range kv {
		if _, exists := os.LookupEnv(key); exists {
			continue // Explicit environment variables take precedence.
		}
		switch v := val.(type) {
		case string:
			_ = os.Setenv(key, v)
		case float64, bool:
			_ = os.Setenv(key, fmt.Sprint(v)) // JSON number 8080 becomes "8080".
		default:
			log.Printf("config: skipping %s (only strings, numbers, and booleans are supported)", key)
		}
	}
}

func main() {
	loadConfigFile("config.json")
	gin.SetMode(gin.ReleaseMode)
	router := initialize.RegisterRouter()
	host := os.Getenv("SERVER_HOST")
	port := os.Getenv("SERVER_PORT")
	tlsCert := os.Getenv("TLS_CERT")
	tlsKey := os.Getenv("TLS_KEY")

	if host == "" {
		host = "0.0.0.0"
	}
	if port == "" {
		port = os.Getenv("PORT")
		if port == "" {
			port = "8080"
		}
	}

	if tlsCert != "" && tlsKey != "" {
		_ = endless.ListenAndServeTLS(host+":"+port, tlsCert, tlsKey, router)
	} else {
		_ = endless.ListenAndServe(host+":"+port, router)
	}
}
