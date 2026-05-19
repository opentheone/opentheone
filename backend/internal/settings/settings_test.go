package settings

import (
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/opentheone/opentheone/backend/internal/model"
)

func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.SystemSetting{}); err != nil {
		t.Fatal(err)
	}
	return db
}

// GetBool must accept all the common "truthy" spellings users may have hand-
// written into the DB (case-insensitive, trimmed). Without that tolerance the
// `allow_register` toggle silently inverts whenever someone runs UPDATE with
// "True " or "YES".
func TestGetBool_AcceptsCommonForms(t *testing.T) {
	s := New(newTestDB(t))
	for _, v := range []string{"1", "true", "True", "TRUE", " yes ", "on"} {
		_ = s.Set("k", v)
		if !s.GetBool("k", false) {
			t.Errorf("%q should be true", v)
		}
	}
	for _, v := range []string{"0", "false", "False", " no", "off"} {
		_ = s.Set("k", v)
		if s.GetBool("k", true) {
			t.Errorf("%q should be false", v)
		}
	}
}

func TestGetBool_FallbackOnMissingAndJunk(t *testing.T) {
	s := New(newTestDB(t))
	if !s.GetBool("never_set", true) {
		t.Error("missing key must return fallback=true")
	}
	if s.GetBool("never_set", false) {
		t.Error("missing key must return fallback=false")
	}
	_ = s.Set("garbage", "perhaps")
	if !s.GetBool("garbage", true) {
		t.Error("junk value must fall back to true")
	}
	if s.GetBool("garbage", false) {
		t.Error("junk value must fall back to false")
	}
}

func TestSetBool_CanonicalForms(t *testing.T) {
	s := New(newTestDB(t))
	if err := s.SetBool("k", true); err != nil {
		t.Fatal(err)
	}
	if got := s.Get("k"); got != "true" {
		t.Errorf("SetBool(true) stored as %q want \"true\"", got)
	}
	if err := s.SetBool("k", false); err != nil {
		t.Fatal(err)
	}
	if got := s.Get("k"); got != "false" {
		t.Errorf("SetBool(false) stored as %q want \"false\"", got)
	}
}

// Set must upsert: writing the same key twice should not create duplicate rows
// nor error out. This is the path the admin UI hits when toggling
// allow_register more than once.
func TestSet_UpsertsExistingRow(t *testing.T) {
	db := newTestDB(t)
	s := New(db)
	if err := s.Set("k", "v1"); err != nil {
		t.Fatal(err)
	}
	if err := s.Set("k", "v2"); err != nil {
		t.Fatal(err)
	}
	if got := s.Get("k"); got != "v2" {
		t.Errorf("got %q want v2", got)
	}
	var rows int64
	_ = db.Model(&model.SystemSetting{}).Where("key = ?", "k").Count(&rows).Error
	if rows != 1 {
		t.Errorf("expected 1 row, got %d (set is not idempotent)", rows)
	}
}

func TestSeed_DoesNotOverwriteExisting(t *testing.T) {
	s := New(newTestDB(t))
	if err := s.Set(KeyAllowRegister, "false"); err != nil {
		t.Fatal(err)
	}
	if err := s.Seed(map[string]string{KeyAllowRegister: "true"}); err != nil {
		t.Fatal(err)
	}
	if s.Get(KeyAllowRegister) != "false" {
		t.Error("Seed must not overwrite a value the operator already set")
	}
}
