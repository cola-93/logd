package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Admin    AdminConfig    `yaml:"admin"`
	Server   ServerConfig   `yaml:"server"`
	Database DatabaseConfig `yaml:"database"`
	Queue    QueueConfig    `yaml:"queue"`
}

const SystemTimezone = "Asia/Shanghai"

func SystemLocation() *time.Location {
	return time.FixedZone(SystemTimezone, 8*60*60)
}

type AdminConfig struct {
	Password      string `yaml:"password"`
	SessionSecret string `yaml:"session_secret"`
}

type ServerConfig struct {
	Listen                   string `yaml:"listen"`
	ReadHeaderTimeoutSeconds int    `yaml:"read_header_timeout_seconds"`
	ReadTimeoutSeconds       int    `yaml:"read_timeout_seconds"`
	WriteTimeoutSeconds      int    `yaml:"write_timeout_seconds"`
	IdleTimeoutSeconds       int    `yaml:"idle_timeout_seconds"`
	ShutdownTimeoutSeconds   int    `yaml:"shutdown_timeout_seconds"`
}

type DatabaseConfig struct {
	URL                   string `yaml:"url"`
	Host                  string `yaml:"host"`
	Port                  int    `yaml:"port"`
	User                  string `yaml:"user"`
	Password              string `yaml:"password"`
	Name                  string `yaml:"name"`
	SSLMode               string `yaml:"sslmode"`
	MaxConns              int32  `yaml:"max_conns"`
	MinConns              int32  `yaml:"min_conns"`
	ConnectTimeoutSeconds int    `yaml:"connect_timeout_seconds"`
}

type QueueConfig struct {
	Capacity             int `yaml:"capacity"`
	WriteBatchSize       int `yaml:"write_batch_size"`
	FlushIntervalMs      int `yaml:"flush_interval_ms"`
	RetryInitialMs       int `yaml:"retry_initial_ms"`
	RetryMaxMs           int `yaml:"retry_max_ms"`
	ShutdownDrainSeconds int `yaml:"shutdown_drain_seconds"`
}

func Load(path string) (Config, error) {
	var cfg Config

	data, err := os.ReadFile(path)
	if err != nil {
		return cfg, fmt.Errorf("read config: %w", err)
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("parse config: %w", err)
	}

	cfg.applyDefaults()
	if err := cfg.validate(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func (c *Config) applyDefaults() {
	if c.Server.Listen == "" {
		c.Server.Listen = "127.0.0.1:7878"
	}
	if c.Server.ReadHeaderTimeoutSeconds == 0 {
		c.Server.ReadHeaderTimeoutSeconds = 5
	}
	if c.Server.ReadTimeoutSeconds == 0 {
		c.Server.ReadTimeoutSeconds = 30
	}
	if c.Server.WriteTimeoutSeconds == 0 {
		c.Server.WriteTimeoutSeconds = 30
	}
	if c.Server.IdleTimeoutSeconds == 0 {
		c.Server.IdleTimeoutSeconds = 60
	}
	if c.Server.ShutdownTimeoutSeconds == 0 {
		c.Server.ShutdownTimeoutSeconds = 15
	}
	if c.Database.Port == 0 {
		c.Database.Port = 5432
	}
	if c.Database.SSLMode == "" {
		c.Database.SSLMode = "disable"
	}
	if c.Database.MaxConns == 0 {
		c.Database.MaxConns = 10
	}
	if c.Database.MinConns == 0 {
		c.Database.MinConns = 1
	}
	if c.Database.ConnectTimeoutSeconds == 0 {
		c.Database.ConnectTimeoutSeconds = 5
	}
	if c.Queue.Capacity == 0 {
		c.Queue.Capacity = 50000
	}
	if c.Queue.WriteBatchSize == 0 {
		c.Queue.WriteBatchSize = 1000
	}
	if c.Queue.FlushIntervalMs == 0 {
		c.Queue.FlushIntervalMs = 1000
	}
	if c.Queue.RetryInitialMs == 0 {
		c.Queue.RetryInitialMs = 200
	}
	if c.Queue.RetryMaxMs == 0 {
		c.Queue.RetryMaxMs = 5000
	}
	if c.Queue.ShutdownDrainSeconds == 0 {
		c.Queue.ShutdownDrainSeconds = 10
	}
}

func (c Config) validate() error {
	if strings.TrimSpace(c.Admin.Password) == "" {
		return fmt.Errorf("admin.password is required")
	}
	if len(c.Admin.SessionSecret) < 32 {
		return fmt.Errorf("admin.session_secret must contain at least 32 characters")
	}
	if c.Server.Listen == "" {
		return fmt.Errorf("server.listen is required")
	}
	if c.Server.ReadHeaderTimeoutSeconds < 1 ||
		c.Server.ReadTimeoutSeconds < 1 ||
		c.Server.WriteTimeoutSeconds < 1 ||
		c.Server.IdleTimeoutSeconds < 1 ||
		c.Server.ShutdownTimeoutSeconds < 1 {
		return fmt.Errorf("server timeouts must be positive")
	}
	if c.Database.URL == "" {
		if strings.TrimSpace(c.Database.Host) == "" {
			return fmt.Errorf("database.host is required")
		}
		if strings.TrimSpace(c.Database.User) == "" {
			return fmt.Errorf("database.user is required")
		}
		if strings.TrimSpace(c.Database.Name) == "" {
			return fmt.Errorf("database.name is required")
		}
	}
	if c.Database.Port < 1 || c.Database.Port > 65535 {
		return fmt.Errorf("database.port is invalid")
	}
	if c.Database.MaxConns < 1 {
		return fmt.Errorf("database.max_conns must be positive")
	}
	if c.Database.MinConns < 0 || c.Database.MinConns > c.Database.MaxConns {
		return fmt.Errorf("database.min_conns is invalid")
	}
	if c.Queue.Capacity < 1 {
		return fmt.Errorf("queue.capacity must be positive")
	}
	if c.Queue.WriteBatchSize < 1 {
		return fmt.Errorf("queue.write_batch_size must be positive")
	}
	if c.Queue.FlushIntervalMs < 1 {
		return fmt.Errorf("queue.flush_interval_ms must be positive")
	}
	if c.Queue.RetryInitialMs < 1 || c.Queue.RetryMaxMs < c.Queue.RetryInitialMs {
		return fmt.Errorf("queue retry interval is invalid")
	}
	if c.Queue.ShutdownDrainSeconds < 1 {
		return fmt.Errorf("queue.shutdown_drain_seconds must be positive")
	}
	return nil
}

func (c DatabaseConfig) DSN() (string, error) {
	if c.URL != "" {
		return c.URL, nil
	}

	u := &url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(c.User, c.Password),
		Host:   net.JoinHostPort(c.Host, strconv.Itoa(c.Port)),
		Path:   "/" + c.Name,
	}
	query := u.Query()
	query.Set("sslmode", c.SSLMode)
	query.Set("connect_timeout", strconv.Itoa(c.ConnectTimeoutSeconds))
	u.RawQuery = query.Encode()
	return u.String(), nil
}

func (c ServerConfig) ReadHeaderTimeout() time.Duration {
	return time.Duration(c.ReadHeaderTimeoutSeconds) * time.Second
}

func (c ServerConfig) ReadTimeout() time.Duration {
	return time.Duration(c.ReadTimeoutSeconds) * time.Second
}

func (c ServerConfig) WriteTimeout() time.Duration {
	return time.Duration(c.WriteTimeoutSeconds) * time.Second
}

func (c ServerConfig) IdleTimeout() time.Duration {
	return time.Duration(c.IdleTimeoutSeconds) * time.Second
}

func (c ServerConfig) ShutdownTimeout() time.Duration {
	return time.Duration(c.ShutdownTimeoutSeconds) * time.Second
}

func (c QueueConfig) FlushInterval() time.Duration {
	return time.Duration(c.FlushIntervalMs) * time.Millisecond
}

func (c QueueConfig) RetryInitial() time.Duration {
	return time.Duration(c.RetryInitialMs) * time.Millisecond
}

func (c QueueConfig) RetryMax() time.Duration {
	return time.Duration(c.RetryMaxMs) * time.Millisecond
}

func (c QueueConfig) ShutdownDrain() time.Duration {
	return time.Duration(c.ShutdownDrainSeconds) * time.Second
}
