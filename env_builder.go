package main

import (
	"os"
	"path/filepath"
)

// safePassthroughKeys defines the explicit list of non-sensitive host environment variables
// that may be propagated from the supervisor to child agent processes if present.
// Sensitive variables (BOT_TOKENS, ALL_TOKENS, ALLOWED_ADMIN_IDS, API keys, credentials)
// are strictly excluded.
var safePassthroughKeys = []string{
	"USER",
	"LOGNAME",
	"SYSTEM_HOME",
	"TMPDIR",
	"TMP",
	"TEMP",
	"AGY_BINARY",
	"SSH_AUTH_SOCK",
	"HTTP_PROXY",
	"HTTPS_PROXY",
	"NO_PROXY",
	"http_proxy",
	"https_proxy",
	"no_proxy",
	"SSL_CERT_FILE",
	"SSL_CERT_DIR",
	"REQUESTS_CA_BUNDLE",
	"CURL_CA_BUNDLE",
	"GIT_AUTHOR_NAME",
	"GIT_AUTHOR_EMAIL",
	"GIT_COMMITTER_NAME",
	"GIT_COMMITTER_EMAIL",
}

// buildChildEnv constructs an isolated, allowlisted environment slice for child CLI processes.
// It explicitly never copies the full supervisor environment (os.Environ()) to eliminate credential
// leakage (BOT_TOKENS, ALL_TOKENS, ALLOWED_ADMIN_IDS, cloud API keys, and supervisor secrets).
func buildChildEnv(accountHome string, extraVars ...map[string]string) []string {
	envMap := make(map[string]string)

	// 1. Core standardized system defaults
	envMap["LANG"] = "C.UTF-8"
	envMap["LC_ALL"] = "C.UTF-8"
	envMap["TZ"] = "UTC"
	envMap["TERM"] = "xterm-256color"

	// 2. Preserve and sanitize PATH
	hostPath := os.Getenv("PATH")
	if hostPath == "" {
		hostPath = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
	}
	envMap["PATH"] = hostPath

	// 3. Safe passthrough of non-sensitive host variables
	for _, key := range safePassthroughKeys {
		if val := os.Getenv(key); val != "" {
			envMap[key] = val
		}
	}

	// 4. Isolated account profile & cache directories
	if accountHome != "" {
		envMap["HOME"] = accountHome
		envMap["GOPATH"] = getCentralSharedGoDir()
		envMap["GOCACHE"] = filepath.Join(getCentralSharedCacheDir(), "go-build")
		envMap["NPM_CONFIG_CACHE"] = getCentralSharedNpmDir()
		envMap["PIP_CACHE_DIR"] = filepath.Join(getCentralSharedCacheDir(), "pip")
	}

	// 5. Apply any extra variables supplied by caller
	for _, extras := range extraVars {
		for k, v := range extras {
			envMap[k] = v
		}
	}

	// 6. Format as KEY=VALUE slice
	res := make([]string, 0, len(envMap))
	for k, v := range envMap {
		res = append(res, k+"="+v)
	}
	return res
}
