// Package config loads the YAML configuration file.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// ClientSecretEnv overrides the OIDC client secret so it can stay out of
// the config file.
const ClientSecretEnv = "CHATCHAT_OIDC_CLIENT_SECRET"

// Config is the application configuration.
type Config struct {
	// Listen is the HTTP listen address, e.g. ":36760".
	Listen string `yaml:"listen"`
	// DataDir holds the SQLite database.
	DataDir string `yaml:"data_dir"`
	// PublicURL is the origin browsers use to reach the site, e.g.
	// "https://chatchat.example.com". It forms the OIDC redirect URI and
	// the chat links handed out by the API.
	PublicURL string `yaml:"public_url"`
	OIDC      OIDC   `yaml:"oidc"`
	// DevUser, when set, signs every browser request in as this local user
	// and disables OIDC. For local development only.
	DevUser string `yaml:"dev_user"`
	Limits  Limits `yaml:"limits"`
}

// OIDC configures the identity provider (Authelia).
type OIDC struct {
	Issuer       string `yaml:"issuer"`
	ClientID     string `yaml:"client_id"`
	ClientSecret string `yaml:"client_secret"`
}

// Limits are the central settings that clients receive from
// GET /api/v1/meta instead of hard-coding them.
type Limits struct {
	// PresenceLease is how long a participant stays online after its last
	// request.
	PresenceLease time.Duration `yaml:"presence_lease"`
	// IdleDestroyAfter destroys a chat that has had nobody online this long.
	IdleDestroyAfter time.Duration `yaml:"idle_destroy_after"`
	// MaxChatLifetime destroys a chat this long after it was created.
	MaxChatLifetime time.Duration `yaml:"max_chat_lifetime"`
	// WaitMax caps a single long poll. It must be shorter than the lease so
	// a waiting participant stays online.
	WaitMax         time.Duration `yaml:"wait_max"`
	PageSizeDefault int           `yaml:"page_size_default"`
	PageSizeMax     int           `yaml:"page_size_max"`
	// SummaryThreshold is the number of new text messages that triggers a
	// summary.
	SummaryThreshold int `yaml:"summary_threshold"`
}

// DefaultLimits returns the limits used when the file sets none.
func DefaultLimits() Limits {
	return Limits{
		PresenceLease:    90 * time.Second,
		IdleDestroyAfter: 30 * time.Minute,
		MaxChatLifetime:  24 * time.Hour,
		WaitMax:          60 * time.Second,
		PageSizeDefault:  50,
		PageSizeMax:      200,
		SummaryThreshold: 20,
	}
}

// Default returns the configuration used when no file is present.
func Default() Config {
	return Config{
		Listen:  ":36760",
		DataDir: "./data",
		Limits:  DefaultLimits(),
	}
}

// Load reads the YAML file at path on top of the defaults.
// A missing file is not an error; the defaults are used. Unknown keys are
// rejected so a misplaced setting such as dev_user cannot be ignored.
func Load(path string) (Config, error) {
	cfg := Default()
	raw, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return cfg, fmt.Errorf("read config: %w", err)
	default:
		dec := yaml.NewDecoder(bytes.NewReader(raw))
		dec.KnownFields(true)
		if err := dec.Decode(&cfg); err != nil && !errors.Is(err, io.EOF) {
			return cfg, fmt.Errorf("parse config %s: %w", path, err)
		}
	}
	if v := os.Getenv(ClientSecretEnv); v != "" {
		cfg.OIDC.ClientSecret = v
	}
	return cfg, cfg.validate()
}

func (c Config) validate() error {
	if c.Listen == "" {
		return errors.New("config: listen must not be empty")
	}
	if c.DataDir == "" {
		return errors.New("config: data_dir must not be empty")
	}
	if err := c.Limits.validate(); err != nil {
		return err
	}
	if c.PublicURL != "" || c.DevUser == "" {
		u, err := url.Parse(c.PublicURL)
		if c.PublicURL == "" || err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" ||
			(u.Path != "" && u.Path != "/") {
			return errors.New("config: public_url must be an origin such as https://chatchat.example.com")
		}
	}
	if c.DevUser != "" {
		return nil
	}
	if c.OIDC.Issuer == "" || c.OIDC.ClientID == "" || c.OIDC.ClientSecret == "" {
		return fmt.Errorf("config: oidc.issuer, oidc.client_id and oidc.client_secret (or %s) are required unless dev_user is set", ClientSecretEnv)
	}
	return nil
}

func (l Limits) validate() error {
	switch {
	case l.PresenceLease <= 0 || l.IdleDestroyAfter <= 0 || l.MaxChatLifetime <= 0 || l.WaitMax <= 0:
		return errors.New("config: limits durations must be positive")
	case l.WaitMax >= l.PresenceLease:
		return errors.New("config: limits.wait_max must be shorter than limits.presence_lease")
	case l.PageSizeDefault <= 0 || l.PageSizeMax < l.PageSizeDefault:
		return errors.New("config: limits.page_size_default must be positive and at most page_size_max")
	case l.SummaryThreshold <= 0:
		return errors.New("config: limits.summary_threshold must be positive")
	}
	return nil
}
