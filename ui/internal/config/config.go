// Package config owns the immutable startup configuration for the UI BFF.
//
// The BFF deliberately reads its environment once.  A running handler never
// consults process environment again, so a configuration change always needs
// a restart and cannot race an in-flight request.
package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	apiURLEnv       = "MASTARR_UI_API_URL"
	listenAddrEnv   = "MASTARR_UI_LISTEN_ADDR"
	publicOriginEnv = "MASTARR_UI_PUBLIC_ORIGIN"
	defaultListen   = ":8081"
)

// Config is the validated, read-once configuration used by the BFF.
//
// The struct follows the repository's typed environment contract.  The UI
// module intentionally keeps this parser dependency-free: ui/go.mod and its
// dependency pins are owned by U-00, so this module does not silently add an
// unpinned environment-parser dependency.
type Config struct {
	// APIURL is the configured Mastarr API origin. It contains no credentials.
	APIURL string
	// ListenAddr is the local BFF listener address.
	ListenAddr string
	// PublicOrigin is the configured HTTPS origin used for canonical metadata.
	PublicOrigin string
	// Source identifies where these values came from for the shell guidance.
	Source string
	// RestartGuidance is intentionally fixed and never includes configuration
	// values or secret material.
	RestartGuidance string
}

// Environment documents the UI bootstrap variables in a typed form.  Parse
// and Load perform the same validation as the env/v11 contract used by the
// root binary while keeping this independently versioned module buildable.
type Environment struct {
	APIURL       string `env:"MASTARR_UI_API_URL,required"`
	ListenAddr   string `env:"MASTARR_UI_LISTEN_ADDR" envDefault:":8081"`
	PublicOrigin string `env:"MASTARR_UI_PUBLIC_ORIGIN,required"`
}

// Load reads the process environment once and returns an immutable snapshot.
func Load() (Config, error) {
	values := map[string]string{}
	for _, key := range []string{apiURLEnv, listenAddrEnv, publicOriginEnv} {
		if value, ok := os.LookupEnv(key); ok {
			values[key] = value
		}
	}
	return Parse(values)
}

// Parse validates an environment snapshot. Missing required values and all
// invalid values are reported by variable name only; raw values are never
// included in errors or the returned guidance.
func Parse(values map[string]string) (Config, error) {
	apiURL, ok := values[apiURLEnv]
	if !ok || strings.TrimSpace(apiURL) == "" {
		return Config{}, fmt.Errorf("%s is required", apiURLEnv)
	}
	listenAddr := values[listenAddrEnv]
	if strings.TrimSpace(listenAddr) == "" {
		listenAddr = defaultListen
	}
	publicOrigin, ok := values[publicOriginEnv]
	if !ok || strings.TrimSpace(publicOrigin) == "" {
		return Config{}, fmt.Errorf("%s is required", publicOriginEnv)
	}

	if err := validateAPIURL(apiURL); err != nil {
		return Config{}, fmt.Errorf("%s is invalid: %w", apiURLEnv, err)
	}
	if err := validateListenAddr(listenAddr); err != nil {
		return Config{}, fmt.Errorf("%s is invalid: %w", listenAddrEnv, err)
	}
	normalizedOrigin, err := normalizePublicOrigin(publicOrigin)
	if err != nil {
		return Config{}, fmt.Errorf("%s is invalid: %w", publicOriginEnv, err)
	}

	return Config{
		APIURL:          normalizeAPIURL(apiURL),
		ListenAddr:      strings.TrimSpace(listenAddr),
		PublicOrigin:    normalizedOrigin,
		Source:          "startup environment",
		RestartGuidance: "UI configuration is read at startup; restart the BFF after changing MASTARR_UI_* values.",
	}, nil
}

func validateAPIURL(raw string) error {
	if !utf8.ValidString(raw) || strings.TrimSpace(raw) != raw {
		return errors.New("must be valid UTF-8 without surrounding whitespace")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed == nil {
		return errors.New("must be an absolute HTTP(S) URL")
	}
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.Hostname() == "" {
		return errors.New("must be an absolute HTTP(S) URL")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Opaque != "" {
		return errors.New("must not contain credentials, query data, or fragments")
	}
	if strings.ContainsAny(parsed.Host, "\r\n\t") {
		return errors.New("contains invalid host data")
	}
	if err := validateURLPort(parsed.Port()); err != nil {
		return err
	}
	return nil
}

func normalizeAPIURL(raw string) string {
	return strings.TrimRight(raw, "/")
}

func validateListenAddr(raw string) error {
	if !utf8.ValidString(raw) || strings.TrimSpace(raw) != raw || strings.ContainsAny(raw, "\r\n\t") {
		return errors.New("must be a valid TCP listen address")
	}
	if strings.ContainsAny(raw, "/\\") {
		return errors.New("must be a TCP listen address")
	}
	host, port, err := net.SplitHostPort(raw)
	if err != nil || (host == "" && port == "") || port == "" {
		return errors.New("must include a TCP port")
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 0 || portNumber > 65535 {
		return errors.New("must include a valid TCP port")
	}
	return nil
}

func normalizePublicOrigin(raw string) (string, error) {
	if !utf8.ValidString(raw) || strings.TrimSpace(raw) != raw {
		return "", errors.New("must be valid UTF-8 without surrounding whitespace")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed == nil {
		return "", errors.New("must be an absolute HTTPS origin")
	}
	if parsed.Scheme != "https" || parsed.Host == "" || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Opaque != "" {
		return "", errors.New("must be an absolute HTTPS origin without credentials or query data")
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return "", errors.New("must not contain a path")
	}
	if err := validateURLPort(parsed.Port()); err != nil {
		return "", err
	}
	parsed.Path = ""
	parsed.RawPath = ""
	return strings.TrimRight(parsed.String(), "/"), nil
}

func validateURLPort(port string) error {
	if port == "" {
		return nil
	}
	value, err := strconv.Atoi(port)
	if err != nil || value < 0 || value > 65535 {
		return errors.New("must include a valid TCP port")
	}
	return nil
}
