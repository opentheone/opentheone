package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/viper"
)

type ServerConfig struct {
	Listen  string `mapstructure:"listen"`
	BaseURL string `mapstructure:"base_url"`
}

type DatabaseConfig struct {
	Driver string `mapstructure:"driver"`
	DSN    string `mapstructure:"dsn"`
}

type AuthConfig struct {
	JWTSecret      string `mapstructure:"jwt_secret"`
	JWTExpireHours int    `mapstructure:"jwt_expire_hours"`
	AllowRegister  bool   `mapstructure:"allow_register"`
}

type ILinkConfig struct {
	BaseURL         string `mapstructure:"base_url"`
	CDNBaseURL      string `mapstructure:"cdn_base_url"`
	ChannelVersion  string `mapstructure:"channel_version"`
	LongPollTimeout int    `mapstructure:"long_poll_timeout_ms"`
	UserAgent       string `mapstructure:"user_agent"`
	SKRouteTag      string `mapstructure:"sk_route_tag"`
}

type StorageConfig struct {
	DataDir        string `mapstructure:"data_dir"`
	AttachmentsDir string `mapstructure:"attachments_dir"`
}

type LoggingConfig struct {
	Level  string `mapstructure:"level"`
	Format string `mapstructure:"format"`
}

type Config struct {
	Server   ServerConfig   `mapstructure:"server"`
	Database DatabaseConfig `mapstructure:"database"`
	Auth     AuthConfig     `mapstructure:"auth"`
	ILink    ILinkConfig    `mapstructure:"ilink"`
	Storage  StorageConfig  `mapstructure:"storage"`
	Logging  LoggingConfig  `mapstructure:"logging"`
}

func setDefaults(v *viper.Viper) {
	v.SetDefault("server.listen", ":8080")
	v.SetDefault("server.base_url", "http://localhost:8080")

	v.SetDefault("database.driver", "sqlite")
	v.SetDefault("database.dsn", "data/oto.db")

	v.SetDefault("auth.jwt_secret", "")
	v.SetDefault("auth.jwt_expire_hours", 168)
	v.SetDefault("auth.allow_register", true)

	v.SetDefault("ilink.base_url", "https://ilinkai.weixin.qq.com")
	v.SetDefault("ilink.cdn_base_url", "https://novac2c.cdn.weixin.qq.com/c2c")
	v.SetDefault("ilink.channel_version", "1.0.0")
	v.SetDefault("ilink.long_poll_timeout_ms", 35000)
	v.SetDefault("ilink.user_agent", "opentheone/0.1")

	v.SetDefault("storage.data_dir", "data")
	v.SetDefault("storage.attachments_dir", "data/attachments")

	v.SetDefault("logging.level", "info")
	v.SetDefault("logging.format", "console")
}

// Load reads config from file (optional), env vars prefixed with OTO_, and defaults.
func Load(path string) (*Config, error) {
	v := viper.New()
	setDefaults(v)

	v.SetEnvPrefix("OTO")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	if path != "" {
		v.SetConfigFile(path)
		if err := v.ReadInConfig(); err != nil {
			if !os.IsNotExist(err) {
				return nil, fmt.Errorf("read config file %s: %w", path, err)
			}
		}
	} else {
		v.SetConfigName("config")
		v.SetConfigType("yaml")
		v.AddConfigPath(".")
		v.AddConfigPath("./config")
		_ = v.ReadInConfig()
	}

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}

	if err := ensureDirs(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func ensureDirs(cfg *Config) error {
	// 0o750: owner full + group read/exec + world none. data/ holds the SQLite
	// db, encrypted LLM API keys, the JWT signing root, and message attachments.
	// Anything more open is gratuitous attack surface on a multi-user host.
	const dirPerm os.FileMode = 0o750
	if cfg.Storage.DataDir != "" {
		if err := os.MkdirAll(cfg.Storage.DataDir, dirPerm); err != nil {
			return fmt.Errorf("ensure data dir: %w", err)
		}
	}
	if cfg.Storage.AttachmentsDir != "" {
		if err := os.MkdirAll(cfg.Storage.AttachmentsDir, dirPerm); err != nil {
			return fmt.Errorf("ensure attachments dir: %w", err)
		}
	}
	if cfg.Database.Driver == "sqlite" && cfg.Database.DSN != "" {
		dir := filepath.Dir(cfg.Database.DSN)
		if dir != "" && dir != "." {
			if err := os.MkdirAll(dir, dirPerm); err != nil {
				return fmt.Errorf("ensure sqlite dir: %w", err)
			}
		}
	}
	return nil
}
