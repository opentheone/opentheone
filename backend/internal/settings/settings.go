package settings

import (
	"errors"
	"strings"

	"gorm.io/gorm"

	"github.com/wzyjerry/opentheone/backend/internal/model"
)

// Keys for runtime-mutable settings.
const (
	KeyAllowRegister = "allow_register"
)

// Service is a thin wrapper around the system_settings table.
type Service struct {
	db *gorm.DB
}

func New(db *gorm.DB) *Service {
	return &Service{db: db}
}

// Seed inserts default rows for any setting that doesn't already exist.
// Call this once on boot. The boolean defaults pass through `defaults` map.
func (s *Service) Seed(defaults map[string]string) error {
	for k, v := range defaults {
		var existing model.SystemSetting
		err := s.db.Where("key = ?", k).First(&existing).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if err := s.db.Create(&model.SystemSetting{Key: k, Value: v}).Error; err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
	}
	return nil
}

// GetBool returns the configured boolean value for a key, or fallback when missing/invalid.
func (s *Service) GetBool(key string, fallback bool) bool {
	var row model.SystemSetting
	if err := s.db.Where("key = ?", key).First(&row).Error; err != nil {
		return fallback
	}
	switch strings.ToLower(strings.TrimSpace(row.Value)) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	return fallback
}

// SetBool persists a boolean value as canonical "true"/"false".
func (s *Service) SetBool(key string, value bool) error {
	v := "false"
	if value {
		v = "true"
	}
	return s.Set(key, v)
}

// Get returns the raw string value or "" when missing.
func (s *Service) Get(key string) string {
	var row model.SystemSetting
	if err := s.db.Where("key = ?", key).First(&row).Error; err != nil {
		return ""
	}
	return row.Value
}

// Set upserts a key/value row.
func (s *Service) Set(key, value string) error {
	var row model.SystemSetting
	err := s.db.Where("key = ?", key).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return s.db.Create(&model.SystemSetting{Key: key, Value: value}).Error
	}
	if err != nil {
		return err
	}
	return s.db.Model(&row).Update("value", value).Error
}
