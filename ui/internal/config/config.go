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

	"github.com/caarlos0/env/v11"
)

const (
	apiURLEnv       = "MASTARR_UI_API_URL"
	listenAddrEnv   = "MASTARR_UI_LISTEN_ADDR"
	publicOriginEnv = "MASTARR_UI_PUBLIC_ORIGIN"
	defaultListen   = ":8081"
	// StartupRestartGuidance is safe to include in configuration diagnostics.
	// It intentionally names no configured value.
	StartupRestartGuidance = "UI configuration is read at startup; restart the BFF after changing MASTARR_UI_* values."
)

// Config is the validated, read-once configuration used by the BFF.
//
// The struct follows the repository's typed environment contract and is
// parsed with caarlos0/env. Its values are copied into the immutable Config
// snapshot returned by Load or Parse.
type Config struct {
	// APIURL is the configured Mastarr API origin. It contains no credentials.
	APIURL string
	// ListenAddr is the local BFF listener address.
	ListenAddr string
	// PublicOrigin is the configured origin used for canonical metadata. HTTPS
	// is required in deployed environments; HTTP is accepted for local use.
	PublicOrigin string
	// Source identifies where these values came from for the shell guidance.
	Source string
	// RestartGuidance is intentionally fixed and never includes configuration
	// values or secret material.
	RestartGuidance string
}

// Environment documents the UI bootstrap variables in a typed form. Parse and
// Load use the same env/v11 contract and validation policy as the root
// bootstrap while keeping this independently versioned module buildable.
type Environment struct {
	APIURL       string `env:"MASTARR_UI_API_URL,required"`
	ListenAddr   string `env:"MASTARR_UI_LISTEN_ADDR" envDefault:":8081"`
	PublicOrigin string `env:"MASTARR_UI_PUBLIC_ORIGIN,required"`
}

// Load reads the process environment once and returns an immutable snapshot.
func Load() (Config, error) {
	return Parse(env.ToMap(os.Environ()))
}

// Parse validates an environment snapshot. Missing required values and all
// invalid values are reported by variable name only; raw values are never
// included in errors or the returned guidance.
func Parse(values map[string]string) (Config, error) {
	if values == nil {
		values = map[string]string{}
	}
	environment, err := env.ParseAsWithOptions[Environment](env.Options{Environment: values})
	if err != nil {
		return Config{}, sanitizeParseError(err)
	}
	if err := environment.Validate(); err != nil {
		return Config{}, err
	}
	normalizedOrigin, err := normalizePublicOrigin(environment.PublicOrigin)
	if err != nil {
		return Config{}, fmt.Errorf("%s: %w", publicOriginEnv, err)
	}

	return Config{
		APIURL:          normalizeAPIURL(environment.APIURL),
		ListenAddr:      strings.TrimSpace(environment.ListenAddr),
		PublicOrigin:    normalizedOrigin,
		Source:          "startup environment",
		RestartGuidance: StartupRestartGuidance,
	}, nil
}

// Validate applies the same URL and listener policy as the root bootstrap
// contract. The API URL may carry an application path, while a public origin
// is an origin only and may not carry a path, query or fragment.
func (e Environment) Validate() error {
	if err := validateAPIURL(e.APIURL); err != nil {
		return fmt.Errorf("%s: %w", apiURLEnv, err)
	}
	if err := validateListenAddr(e.ListenAddr); err != nil {
		return fmt.Errorf("%s: %w", listenAddrEnv, err)
	}
	if err := validatePublicOrigin(e.PublicOrigin); err != nil {
		return fmt.Errorf("%s: %w", publicOriginEnv, err)
	}
	return nil
}

func sanitizeParseError(err error) error {
	var parseErr env.ParseError
	if errors.As(err, &parseErr) {
		return fmt.Errorf("invalid environment field %q", parseErr.Name)
	}
	var missingErr env.VarIsNotSetError
	if errors.As(err, &missingErr) {
		return fmt.Errorf("required environment variable %q is not set", missingErr.Key)
	}
	var emptyErr env.EmptyVarError
	if errors.As(err, &emptyErr) {
		return fmt.Errorf("environment variable %q must not be empty", emptyErr.Key)
	}
	return errors.New("invalid environment configuration")
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
		return "", errors.New("must be an absolute HTTP(S) origin")
	}
	if err := validatePublicOrigin(raw); err != nil {
		return "", err
	}
	parsed.Path = ""
	parsed.RawPath = ""
	return strings.TrimRight(parsed.String(), "/"), nil
}

func validatePublicOrigin(raw string) error {
	if !utf8.ValidString(raw) || strings.TrimSpace(raw) != raw {
		return errors.New("must be valid UTF-8 without surrounding whitespace")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed == nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.Hostname() == "" {
		return errors.New("must be an absolute HTTP(S) origin")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Opaque != "" {
		return errors.New("must not contain credentials, query data, or fragments")
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return errors.New("must not contain a path")
	}
	if strings.ContainsAny(parsed.Host, "\r\n\t") {
		return errors.New("contains invalid host data")
	}
	if err := validateURLPort(parsed.Port()); err != nil {
		return err
	}
	return nil
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
