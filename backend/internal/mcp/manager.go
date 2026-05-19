package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	mcpproto "github.com/mark3labs/mcp-go/mcp"
	"go.uber.org/zap"

	"github.com/opentheone/opentheone/backend/internal/model"
)

const (
	// initTimeout caps the initialize+listTools handshake.
	initTimeout = 15 * time.Second
	// defaultCallTimeout is used if MCPServer.TimeoutMs is zero/negative.
	defaultCallTimeout = 30 * time.Second
	// maxIdle caps how long a cached connection can stay open without use
	// before the next acquire will tear it down and reconnect. Protects
	// against half-dead stdio subprocesses.
	maxIdle = 30 * time.Minute
)

// Manager owns a process-wide cache of live MCP client connections.
// Each MCPServer row maps to at most one cached entry; the entry is keyed
// by id+config_hash so an edit to the row will invalidate the connection.
type Manager struct {
	log *zap.Logger
	mu  sync.Mutex
	// entries are keyed by MCPServer.ID.
	entries map[string]*cached
	// janitor lifecycle. janitorOnce guards the launch; janitorStopOnce
	// guards the channel close so Shutdown can honor its idempotency
	// contract under concurrent calls (close-of-closed-channel panics).
	janitorOnce     sync.Once
	janitorStopOnce sync.Once
	janitorStop     chan struct{}
}

type cached struct {
	client     *mcpclient.Client
	transport  transport.Interface
	configHash string
	tools      []mcpproto.Tool
	lastUsed   time.Time
}

// NewManager constructs an empty manager. Connections are opened lazily on
// the first Get call. A background janitor proactively reaps entries idle
// past maxIdle so a disabled / forgotten stdio MCP subprocess doesn't keep
// running for the lifetime of the host process.
func NewManager(log *zap.Logger) *Manager {
	m := &Manager{
		log:         log.With(zap.String("subsys", "mcp")),
		entries:     make(map[string]*cached),
		janitorStop: make(chan struct{}),
	}
	m.startJanitor()
	return m
}

// janitorInterval is how often the idle-eviction sweep runs. Picked at
// maxIdle / 6 so a freshly idle entry is reaped within ~5 minutes after
// crossing the threshold. Cheaper than every-minute scans and still tight
// enough that operators don't notice stdio subprocesses hanging around.
const janitorInterval = 5 * time.Minute

// startJanitor launches the idle-eviction goroutine exactly once per Manager.
// Without it, the documented maxIdle is only enforced lazily on the next
// acquire() — meaning a stdio server that nobody is currently using would
// keep its subprocess alive forever. The janitor closes entries whose
// `lastUsed` is past maxIdle, mirroring the same teardown path acquire uses.
func (m *Manager) startJanitor() {
	m.janitorOnce.Do(func() {
		go func() {
			defer func() {
				if rec := recover(); rec != nil {
					m.log.Error("mcp janitor panicked", zap.Any("panic", rec))
				}
			}()
			t := time.NewTicker(janitorInterval)
			defer t.Stop()
			for {
				select {
				case <-m.janitorStop:
					return
				case <-t.C:
					m.evictStale()
				}
			}
		}()
	})
}

// evictStale closes and removes cached entries whose lastUsed exceeded
// maxIdle. Called by the janitor on a timer; safe to call from anywhere.
func (m *Manager) evictStale() {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	for id, e := range m.entries {
		if now.Sub(e.lastUsed) > maxIdle {
			m.log.Info("mcp evicting idle entry",
				zap.String("server_id", id),
				zap.Duration("idle", now.Sub(e.lastUsed)))
			closeEntry(m.log, id, e)
			delete(m.entries, id)
		}
	}
}

// Shutdown closes every cached connection and stops the janitor.
// Safe to call multiple times (sync.Once guards the channel close; the
// entry-teardown loop is naturally idempotent because we reset the map at
// the end).
func (m *Manager) Shutdown() {
	// Signal the janitor first so it can't race with the entry teardown
	// below. Guarding with sync.Once is the only race-free way to make a
	// channel close idempotent — the select+default pattern has a TOCTOU
	// window where two concurrent shutdowns can both observe "not yet
	// closed" and both call close(), panicking the second one.
	m.janitorStopOnce.Do(func() { close(m.janitorStop) })
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, e := range m.entries {
		closeEntry(m.log, id, e)
	}
	m.entries = make(map[string]*cached)
}

// Invalidate forces the next Get to reconnect for this server. Use after
// editing or deleting the MCPServer row.
func (m *Manager) Invalidate(serverID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e, ok := m.entries[serverID]; ok {
		closeEntry(m.log, serverID, e)
		delete(m.entries, serverID)
	}
}

// ListTools returns the cached tool descriptors for one server, opening the
// connection if needed. The returned slice is owned by the manager — do not
// mutate.
func (m *Manager) ListTools(ctx context.Context, srv *model.MCPServer) ([]mcpproto.Tool, error) {
	c, err := m.acquire(ctx, srv)
	if err != nil {
		return nil, err
	}
	return c.tools, nil
}

// CallTool invokes a tool on a specific server. timeoutMs comes from the
// MCPServer row; zero/negative falls back to defaultCallTimeout.
func (m *Manager) CallTool(ctx context.Context, srv *model.MCPServer, toolName string, args map[string]any) (*mcpproto.CallToolResult, error) {
	c, err := m.acquire(ctx, srv)
	if err != nil {
		return nil, err
	}
	d := time.Duration(srv.TimeoutMs) * time.Millisecond
	if d <= 0 {
		d = defaultCallTimeout
	}
	cctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	req := mcpproto.CallToolRequest{}
	req.Params.Name = toolName
	if args == nil {
		args = map[string]any{}
	}
	req.Params.Arguments = args
	return c.client.CallTool(cctx, req)
}

// Probe opens (or re-opens) a connection to verify the MCPServer config is
// usable. Caller-friendly: the returned tool list lets the UI show "OK,
// 7 tools available" right after the user saves.
//
// Probe always forces a fresh connect: if the config_hash changed, the old
// entry is invalidated first; otherwise we still close+reopen so the user
// gets a real connectivity signal, not a cached "yes" from 10 minutes ago.
func (m *Manager) Probe(ctx context.Context, srv *model.MCPServer) ([]mcpproto.Tool, error) {
	m.Invalidate(srv.ID)
	return m.ListTools(ctx, srv)
}

func (m *Manager) acquire(ctx context.Context, srv *model.MCPServer) (*cached, error) {
	if srv == nil {
		return nil, errors.New("mcp: nil server")
	}
	if !srv.Enabled {
		return nil, fmt.Errorf("mcp: server %q is disabled", srv.Name)
	}
	hash := configHash(srv)

	m.mu.Lock()
	if e, ok := m.entries[srv.ID]; ok {
		stale := e.configHash != hash || time.Since(e.lastUsed) > maxIdle
		if !stale {
			e.lastUsed = time.Now()
			m.mu.Unlock()
			return e, nil
		}
		// Stale: tear down and fall through to reconnect.
		closeEntry(m.log, srv.ID, e)
		delete(m.entries, srv.ID)
	}
	m.mu.Unlock()

	// Connect outside the lock; transports can take a few seconds.
	entry, err := m.connect(ctx, srv)
	if err != nil {
		return nil, err
	}
	entry.configHash = hash
	entry.lastUsed = time.Now()

	m.mu.Lock()
	defer m.mu.Unlock()
	// Another goroutine may have raced us; if so, drop ours, use theirs.
	if existing, ok := m.entries[srv.ID]; ok {
		closeEntry(m.log, srv.ID, entry)
		existing.lastUsed = time.Now()
		return existing, nil
	}
	m.entries[srv.ID] = entry
	return entry, nil
}

func (m *Manager) connect(ctx context.Context, srv *model.MCPServer) (*cached, error) {
	var (
		cli *mcpclient.Client
		trn transport.Interface
		err error
	)
	switch strings.ToLower(strings.TrimSpace(srv.Transport)) {
	case "", "stdio":
		if strings.TrimSpace(srv.Command) == "" {
			return nil, fmt.Errorf("mcp: stdio server %q has empty command", srv.Name)
		}
		args := DecodeArgs(srv.Args)
		env := EnvSlice(DecodeMap(srv.Env))
		cli, err = mcpclient.NewStdioMCPClient(srv.Command, env, args...)
		if err != nil {
			return nil, fmt.Errorf("mcp: stdio start %q: %w", srv.Name, err)
		}
	case "streamable_http", "http":
		if strings.TrimSpace(srv.URL) == "" {
			return nil, fmt.Errorf("mcp: streamable_http server %q has empty url", srv.Name)
		}
		opts := []transport.StreamableHTTPCOption{
			transport.WithHTTPTimeout(60 * time.Second),
		}
		if headers := DecodeMap(srv.Headers); len(headers) > 0 {
			opts = append(opts, transport.WithHTTPHeaders(headers))
		}
		cli, err = mcpclient.NewStreamableHttpClient(srv.URL, opts...)
		if err != nil {
			return nil, fmt.Errorf("mcp: http connect %q: %w", srv.Name, err)
		}
	default:
		return nil, fmt.Errorf("mcp: unknown transport %q", srv.Transport)
	}
	trn = cli.GetTransport()

	ictx, cancel := context.WithTimeout(ctx, initTimeout)
	defer cancel()

	// Stdio client auto-starts; HTTP client may need Start. Calling Start on
	// an already-started transport is harmless per upstream.
	if err := cli.Start(ictx); err != nil {
		_ = cli.Close()
		return nil, fmt.Errorf("mcp: start %q: %w", srv.Name, err)
	}

	initReq := mcpproto.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcpproto.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcpproto.Implementation{
		Name:    "opentheone",
		Version: "0.1",
	}
	if _, err := cli.Initialize(ictx, initReq); err != nil {
		_ = cli.Close()
		return nil, fmt.Errorf("mcp: initialize %q: %w", srv.Name, err)
	}

	tres, err := cli.ListTools(ictx, mcpproto.ListToolsRequest{})
	if err != nil {
		_ = cli.Close()
		return nil, fmt.Errorf("mcp: list_tools %q: %w", srv.Name, err)
	}
	return &cached{
		client:    cli,
		transport: trn,
		tools:     tres.Tools,
	}, nil
}

// configHash is a cheap fingerprint of the config fields a transport depends
// on, used to invalidate cached connections when the user edits a server.
func configHash(srv *model.MCPServer) string {
	type snapshot struct {
		Transport string
		Command   string
		Args      string
		Env       string
		URL       string
		Headers   string
		TimeoutMs int
	}
	buf, _ := json.Marshal(snapshot{
		Transport: srv.Transport,
		Command:   srv.Command,
		Args:      srv.Args,
		Env:       srv.Env,
		URL:       srv.URL,
		Headers:   srv.Headers,
		TimeoutMs: srv.TimeoutMs,
	})
	return string(buf)
}

func closeEntry(log *zap.Logger, id string, e *cached) {
	if e == nil || e.client == nil {
		return
	}
	if err := e.client.Close(); err != nil {
		log.Debug("mcp close failed", zap.String("server_id", id), zap.Error(err))
	}
}
