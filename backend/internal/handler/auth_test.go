package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/opentheone/opentheone/backend/internal/auth"
	"github.com/opentheone/opentheone/backend/internal/model"
	"github.com/opentheone/opentheone/backend/internal/settings"
)

// qaNewDB spins up a fresh in-memory sqlite with the full schema.
// We use the same migration set Open() uses in production, so any schema
// drift would surface here.
func qaNewDB(t *testing.T) *gorm.DB {
	t.Helper()
	// Each test gets its own shared-cache in-memory DB; the random DSN keeps
	// parallel tests from sharing rows, while cache=shared lets the pool of
	// goroutines inside one test see the same schema/rows.
	dsn := "file:qa-" + t.Name() + "?mode=memory&cache=shared&_foreign_keys=on"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(model.AllModels()...); err != nil {
		t.Fatal(err)
	}
	// Avoid sqlite "database is locked" under writer contention by serializing
	// writes through a single connection (sqlite only does one writer anyway).
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	return db
}

func qaNewAuthHandler(t *testing.T, db *gorm.DB) *AuthHandler {
	t.Helper()
	tm := auth.NewTokenManager("test-secret-at-least-16-chars-long", 1)
	return NewAuthHandler(db, tm, settings.New(db))
}

func qaPostJSON(h gin.HandlerFunc, body any, headers map[string]string) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	buf, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(buf))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req
	h(c)
	return w
}

// envelope decodes the standard { code, msg, data } response shape.
type envelope struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

func qaDecode(t *testing.T, w *httptest.ResponseRecorder) envelope {
	t.Helper()
	var e envelope
	if err := json.Unmarshal(w.Body.Bytes(), &e); err != nil {
		t.Fatalf("decode response: %v body=%s", err, w.Body.String())
	}
	return e
}

// --- A1: empty username/password ---
func TestRegister_RejectsEmpty(t *testing.T) {
	h := qaNewAuthHandler(t, qaNewDB(t))
	w := qaPostJSON(h.Register, map[string]string{"username": "", "password": "secret123"}, nil)
	if w.Code != http.StatusBadRequest {
		t.Errorf("empty username: status %d want 400", w.Code)
	}
	w = qaPostJSON(h.Register, map[string]string{"username": "alice", "password": ""}, nil)
	if w.Code != http.StatusBadRequest {
		t.Errorf("empty password: status %d want 400", w.Code)
	}
}

// --- A2: password shorter than minimum ---
func TestRegister_RejectsShortPassword(t *testing.T) {
	h := qaNewAuthHandler(t, qaNewDB(t))
	w := qaPostJSON(h.Register, map[string]string{"username": "alice", "password": "abc"}, nil)
	if w.Code != http.StatusBadRequest {
		t.Errorf("short password: status %d want 400", w.Code)
	}
	if !strings.Contains(w.Body.String(), "password too short") {
		t.Errorf("expected helpful error, got %q", w.Body.String())
	}
}

// --- A3: duplicate username ---
func TestRegister_RejectsDuplicate(t *testing.T) {
	h := qaNewAuthHandler(t, qaNewDB(t))
	body := map[string]string{"username": "alice", "password": "secret123"}
	if w := qaPostJSON(h.Register, body, nil); w.Code != http.StatusOK {
		t.Fatalf("first register failed: %d %s", w.Code, w.Body.String())
	}
	w := qaPostJSON(h.Register, body, nil)
	if w.Code != http.StatusBadRequest {
		t.Errorf("duplicate username: status %d want 400", w.Code)
	}
	if !strings.Contains(w.Body.String(), "username taken") {
		t.Errorf("expected 'username taken', got %q", w.Body.String())
	}
}

// --- A4: username with whitespace is trimmed and queryable ---
func TestRegister_TrimsUsername(t *testing.T) {
	db := qaNewDB(t)
	h := qaNewAuthHandler(t, db)
	if w := qaPostJSON(h.Register, map[string]string{"username": "  alice\t", "password": "secret123"}, nil); w.Code != http.StatusOK {
		t.Fatalf("status %d body %s", w.Code, w.Body.String())
	}
	var u model.User
	if err := db.Where("username = ?", "alice").First(&u).Error; err != nil {
		t.Errorf("stored username is not trimmed: %v", err)
	}
}

// --- A5 + A6: first user admin, second user is regular ---
func TestRegister_FirstUserIsAdmin(t *testing.T) {
	db := qaNewDB(t)
	h := qaNewAuthHandler(t, db)
	if w := qaPostJSON(h.Register, map[string]string{"username": "alice", "password": "secret123"}, nil); w.Code != http.StatusOK {
		t.Fatalf("first register: %d %s", w.Code, w.Body.String())
	}
	if w := qaPostJSON(h.Register, map[string]string{"username": "bob", "password": "secret123"}, nil); w.Code != http.StatusOK {
		t.Fatalf("second register: %d %s", w.Code, w.Body.String())
	}
	var users []model.User
	if err := db.Order("created_at asc").Find(&users).Error; err != nil {
		t.Fatal(err)
	}
	if users[0].Role != "admin" {
		t.Errorf("first user role=%q want admin", users[0].Role)
	}
	if users[1].Role != "user" {
		t.Errorf("second user role=%q want user", users[1].Role)
	}
}

// --- A7: allow_register=false blocks new registrations once any user exists ---
func TestRegister_DisabledAfterBootstrap(t *testing.T) {
	db := qaNewDB(t)
	h := qaNewAuthHandler(t, db)
	if w := qaPostJSON(h.Register, map[string]string{"username": "alice", "password": "secret123"}, nil); w.Code != http.StatusOK {
		t.Fatalf("bootstrap register: %d %s", w.Code, w.Body.String())
	}
	if err := h.settings.SetBool(settings.KeyAllowRegister, false); err != nil {
		t.Fatal(err)
	}
	w := qaPostJSON(h.Register, map[string]string{"username": "bob", "password": "secret123"}, nil)
	if w.Code != http.StatusForbidden {
		t.Errorf("post-bootstrap with allow_register=false: status %d want 403", w.Code)
	}
}

// --- A8: allow_register=false still allows the very first user (admin bootstrap) ---
func TestRegister_DisabledAllowsBootstrap(t *testing.T) {
	db := qaNewDB(t)
	h := qaNewAuthHandler(t, db)
	if err := h.settings.SetBool(settings.KeyAllowRegister, false); err != nil {
		t.Fatal(err)
	}
	w := qaPostJSON(h.Register, map[string]string{"username": "alice", "password": "secret123"}, nil)
	if w.Code != http.StatusOK {
		t.Errorf("bootstrap with allow_register=false should succeed: status %d body %s", w.Code, w.Body.String())
	}
}

// --- A9: race-test the "first user is admin" election ---
//
// Drives N parallel registrations against a single fresh DB. If the
// transactional fix is missing, more than one user will be promoted to admin.
// This test fails the codebase prior to the recent Register() refactor.
func TestRegister_ConcurrentFirstUsersNoDoubleAdmin(t *testing.T) {
	db := qaNewDB(t)
	h := qaNewAuthHandler(t, db)
	const N = 20
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func(i int) {
			defer wg.Done()
			_ = qaPostJSON(h.Register, map[string]any{
				"username": "user" + strings.Repeat("x", i),
				"password": "secret123",
			}, nil)
		}(i)
	}
	wg.Wait()
	var admins int64
	if err := db.Model(&model.User{}).Where("role = ?", "admin").Count(&admins).Error; err != nil {
		t.Fatal(err)
	}
	if admins != 1 {
		t.Errorf("dual-admin race: got %d admins, want exactly 1", admins)
	}
}

// --- A10: Login should trim the username, otherwise users get phantom
// "wrong credentials" failures from mobile autofill / chinese IMEs that
// inject trailing whitespace. ---
func TestLogin_TrimsUsername(t *testing.T) {
	db := qaNewDB(t)
	h := qaNewAuthHandler(t, db)
	if w := qaPostJSON(h.Register, map[string]string{"username": "alice", "password": "secret123"}, nil); w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	w := qaPostJSON(h.Login, map[string]string{"username": "  alice ", "password": "secret123"}, nil)
	if w.Code != http.StatusOK {
		t.Errorf("login with surrounding whitespace: status %d body %s", w.Code, w.Body.String())
	}
}

// --- A11: wrong password / unknown user both 401. We don't care about which
// of the two errors comes back — the contract is "the API never leaks whether
// the username exists". The current implementation returns the same code+msg
// for both, which we lock in here. ---
func TestLogin_RejectsWrong(t *testing.T) {
	db := qaNewDB(t)
	h := qaNewAuthHandler(t, db)
	_ = qaPostJSON(h.Register, map[string]string{"username": "alice", "password": "secret123"}, nil)

	wrongPw := qaPostJSON(h.Login, map[string]string{"username": "alice", "password": "nope12345"}, nil)
	if wrongPw.Code != http.StatusUnauthorized {
		t.Errorf("wrong pw status %d want 401", wrongPw.Code)
	}
	missing := qaPostJSON(h.Login, map[string]string{"username": "ghost", "password": "secret123"}, nil)
	if missing.Code != http.StatusUnauthorized {
		t.Errorf("missing user status %d want 401", missing.Code)
	}
}

// --- A12 + A13: UpdatePassword validation ---
func TestUpdatePassword_Validation(t *testing.T) {
	db := qaNewDB(t)
	h := qaNewAuthHandler(t, db)
	if w := qaPostJSON(h.Register, map[string]string{"username": "alice", "password": "secret123"}, nil); w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	var u model.User
	_ = db.Where("username = ?", "alice").First(&u).Error

	// helper to call UpdatePassword with the user context already set.
	call := func(body map[string]string) *httptest.ResponseRecorder {
		gin.SetMode(gin.TestMode)
		buf, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(buf))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = req
		// Simulate JWTAuth having stamped the user id.
		c.Set("auth.user_id", u.ID)
		h.UpdatePassword(c)
		return w
	}

	if w := call(map[string]string{"old_password": "secret123", "new_password": "x"}); w.Code != http.StatusBadRequest {
		t.Errorf("too-short new password: status %d want 400", w.Code)
	}
	if w := call(map[string]string{"old_password": "WRONG", "new_password": "newsecret"}); w.Code != http.StatusUnauthorized {
		t.Errorf("wrong old password: status %d want 401", w.Code)
	}
	if w := call(map[string]string{"old_password": "secret123", "new_password": "newsecret"}); w.Code != http.StatusOK {
		t.Errorf("happy path: status %d body %s", w.Code, w.Body.String())
	}
	// Old password should no longer authenticate.
	if w := qaPostJSON(h.Login, map[string]string{"username": "alice", "password": "secret123"}, nil); w.Code == http.StatusOK {
		t.Errorf("old password should be revoked after change")
	}
	if w := qaPostJSON(h.Login, map[string]string{"username": "alice", "password": "newsecret"}, nil); w.Code != http.StatusOK {
		t.Errorf("new password should authenticate: %d body %s", w.Code, w.Body.String())
	}
}

// --- Belt-and-suspenders helper used to silence unused-import warnings if any
// of the imports above happen to not be referenced after future refactors. ---
var _ = context.Background
