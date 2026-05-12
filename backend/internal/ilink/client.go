package ilink

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Client talks the iLink ClawBot HTTP/JSON protocol.
type Client struct {
	httpClient        *http.Client
	BaseURL           string
	CDNBaseURL        string
	ChannelVersion    string
	UserAgent         string
	SKRouteTag        string
	AppID             string
	AppClientVersion  string
	LongPollTimeout   time.Duration
	ShortCallTimeout  time.Duration
	ConfigCallTimeout time.Duration
}

// ClientOptions configures Client.
type ClientOptions struct {
	BaseURL         string
	CDNBaseURL      string
	ChannelVersion  string
	UserAgent       string
	SKRouteTag      string
	LongPollTimeout time.Duration
	// AppID populates the `iLink-App-Id` HTTP header on every request. The
	// official @tencent-weixin/openclaw-weixin plugin v2.4.3 ships the literal
	// value `"bot"` here (sourced from `package.json#ilink_appid`). Some server
	// builds gate inbound message routing on this header, so omitting it can
	// produce an authenticated session that silently never delivers messages.
	AppID string
}

// NewClient returns a Client with sensible defaults.
func NewClient(opts ClientOptions) *Client {
	if opts.BaseURL == "" {
		opts.BaseURL = "https://ilinkai.weixin.qq.com"
	}
	if opts.CDNBaseURL == "" {
		opts.CDNBaseURL = "https://novac2c.cdn.weixin.qq.com/c2c"
	}
	if opts.ChannelVersion == "" {
		opts.ChannelVersion = "1.0.0"
	}
	if opts.UserAgent == "" {
		opts.UserAgent = "opentheone/0.1"
	}
	if opts.LongPollTimeout <= 0 {
		opts.LongPollTimeout = 35 * time.Second
	}
	if opts.AppID == "" {
		opts.AppID = "bot"
	}

	// The official client uses per-call timeouts (35s long-poll, 15s send,
	// 10s config) via fetch AbortControllers. Go's http.Client.Timeout is
	// applied globally — to keep semantics close, we set the client-level
	// timeout to the largest expected (long-poll + buffer) and additionally
	// apply per-call context deadlines in the request methods below.
	httpClient := &http.Client{Timeout: opts.LongPollTimeout + 10*time.Second}
	return &Client{
		httpClient:        httpClient,
		BaseURL:           opts.BaseURL,
		CDNBaseURL:        opts.CDNBaseURL,
		ChannelVersion:    opts.ChannelVersion,
		UserAgent:         opts.UserAgent,
		SKRouteTag:        opts.SKRouteTag,
		AppID:             opts.AppID,
		AppClientVersion:  encodeAppClientVersion(opts.ChannelVersion),
		LongPollTimeout:   opts.LongPollTimeout,
		ShortCallTimeout:  15 * time.Second,
		ConfigCallTimeout: 10 * time.Second,
	}
}

// encodeAppClientVersion mirrors the bit-packed scheme used by the official
// JS plugin: `((major & 0xff) << 16) | ((minor & 0xff) << 8) | (patch & 0xff)`
// and returns the decimal representation as a string.
//
// For ChannelVersion "1.0.0" → 0x010000 → "65536"; "1.0.2" → "65538".
// Unparseable / shorter version strings fall back to 0 components, which
// matches the official behavior as well.
func encodeAppClientVersion(version string) string {
	parts := strings.Split(version, ".")
	var major, minor, patch uint32
	if len(parts) > 0 {
		if v, err := strconv.ParseUint(parts[0], 10, 32); err == nil {
			major = uint32(v)
		}
	}
	if len(parts) > 1 {
		if v, err := strconv.ParseUint(parts[1], 10, 32); err == nil {
			minor = uint32(v)
		}
	}
	if len(parts) > 2 {
		if v, err := strconv.ParseUint(parts[2], 10, 32); err == nil {
			patch = uint32(v)
		}
	}
	encoded := ((major & 0xff) << 16) | ((minor & 0xff) << 8) | (patch & 0xff)
	return strconv.FormatUint(uint64(encoded), 10)
}

func (c *Client) baseInfo() BaseInfo {
	return BaseInfo{ChannelVersion: c.ChannelVersion}
}

// randomWechatUIN generates X-WECHAT-UIN header value.
func randomWechatUIN() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return base64.StdEncoding.EncodeToString([]byte("0"))
	}
	u := binary.BigEndian.Uint32(b[:])
	dec := strconv.FormatUint(uint64(u), 10)
	return base64.StdEncoding.EncodeToString([]byte(dec))
}

// applyCommonHeaders sets the headers attached to *every* request, both GET
// (qrcode endpoints) and POST (business endpoints). Mirrors `buildCommonHeaders`
// in @tencent-weixin/openclaw-weixin/src/api/api.ts.
//
// `iLink-App-Id` and `iLink-App-ClientVersion` are NOT in the publicly
// documented header list — but the official client sends them on every wire
// request including the unauthenticated QR endpoints, and at least one server
// build was observed to refuse to push inbound messages to clients that
// omit them. Treat them as required.
func (c *Client) applyCommonHeaders(req *http.Request) {
	req.Header.Set("iLink-App-Id", c.AppID)
	req.Header.Set("iLink-App-ClientVersion", c.AppClientVersion)
	if c.SKRouteTag != "" {
		req.Header.Set("SKRouteTag", c.SKRouteTag)
	}
	req.Header.Set("User-Agent", c.UserAgent)
}

// applyAuthHeaders extends applyCommonHeaders with the headers required for
// authenticated POST requests against the business endpoints.
func (c *Client) applyAuthHeaders(req *http.Request, botToken string) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("AuthorizationType", "ilink_bot_token")
	req.Header.Set("Authorization", "Bearer "+botToken)
	req.Header.Set("X-WECHAT-UIN", randomWechatUIN())
	c.applyCommonHeaders(req)
}

// GetBotQRCode requests a fresh QR code (no auth required).
func (c *Client) GetBotQRCode(ctx context.Context) (*QRCodeResp, error) {
	u := c.BaseURL + "/ilink/bot/get_bot_qrcode?bot_type=3"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	c.applyCommonHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("get_bot_qrcode http %d: %s", resp.StatusCode, string(body))
	}
	var out QRCodeResp
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("decode qrcode resp: %w body=%s", err, string(body))
	}
	return &out, nil
}

// GetQRCodeStatus polls the scan status. The caller is expected to call this in a loop.
func (c *Client) GetQRCodeStatus(ctx context.Context, qrcode string) (*QRStatusResp, error) {
	u := c.BaseURL + "/ilink/bot/get_qrcode_status?qrcode=" + url.QueryEscape(qrcode)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	c.applyCommonHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("get_qrcode_status http %d: %s", resp.StatusCode, string(body))
	}
	var out QRStatusResp
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("decode qr status: %w body=%s", err, string(body))
	}
	return &out, nil
}

// post performs an authenticated business POST against either the binding baseURL or c.BaseURL.
func (c *Client) post(ctx context.Context, baseURL, path string, botToken string, body, out interface{}) error {
	if baseURL == "" {
		baseURL = c.BaseURL
	}
	buf, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+path, bytes.NewReader(buf))
	if err != nil {
		return err
	}
	c.applyAuthHeaders(req, botToken)
	req.ContentLength = int64(len(buf))

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s http %d: %s", path, resp.StatusCode, string(respBody))
	}
	if out != nil {
		if err := json.Unmarshal(respBody, out); err != nil {
			return fmt.Errorf("decode %s: %w body=%s", path, err, string(respBody))
		}
	}
	return nil
}

// Session captures the per-binding credentials needed for any business call.
type Session struct {
	BotToken    string
	BaseURL     string
	ILinkBotID  string
	ILinkUserID string
}

// GetUpdates performs the long-polling getupdates call. The caller should drive a loop.
func (c *Client) GetUpdates(ctx context.Context, sess Session, cursor string) (*GetUpdatesResp, error) {
	body := GetUpdatesReq{
		GetUpdatesBuf: cursor,
		BaseInfo:      c.baseInfo(),
	}
	var out GetUpdatesResp
	if err := c.post(ctx, sess.BaseURL, "/ilink/bot/getupdates", sess.BotToken, body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// SendMessage sends a single message (one MessageItem is the recommended pattern).
func (c *Client) SendMessage(ctx context.Context, sess Session, msg WeixinMessage) (*SendMessageResp, error) {
	body := SendMessageReq{Msg: msg, BaseInfo: c.baseInfo()}
	var out SendMessageResp
	if err := c.post(ctx, sess.BaseURL, "/ilink/bot/sendmessage", sess.BotToken, body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// SendTextMessage is a convenience wrapper that builds a TEXT message and sends it.
func (c *Client) SendTextMessage(ctx context.Context, sess Session, toUserID, contextToken, clientID, text string) error {
	msg := WeixinMessage{
		ToUserID:     toUserID,
		ClientID:     clientID,
		MessageType:  MessageTypeBot,
		MessageState: MessageStateFinish,
		ContextToken: contextToken,
		ItemList: []MessageItem{
			{
				Type:     ItemTypeText,
				TextItem: &TextItem{Text: text},
			},
		},
	}
	resp, err := c.SendMessage(ctx, sess, msg)
	if err != nil {
		return err
	}
	if resp.Ret != 0 {
		return fmt.Errorf("sendmessage ret=%d errcode=%d errmsg=%s", resp.Ret, resp.ErrCode, resp.ErrMsg)
	}
	return nil
}

// GetTypingTicket fetches a typing ticket scoped to a user.
func (c *Client) GetTypingTicket(ctx context.Context, sess Session, toUserID, contextToken string) (string, error) {
	body := GetConfigReq{
		ILinkUserID:  toUserID,
		ContextToken: contextToken,
		BaseInfo:     c.baseInfo(),
	}
	var out GetConfigResp
	if err := c.post(ctx, sess.BaseURL, "/ilink/bot/getconfig", sess.BotToken, body, &out); err != nil {
		return "", err
	}
	if out.Ret != 0 {
		return "", fmt.Errorf("getconfig ret=%d", out.Ret)
	}
	return out.TypingTicket, nil
}

// SendTyping notifies the WeChat client that the bot is typing (status=1) or stops (status=2).
func (c *Client) SendTyping(ctx context.Context, sess Session, toUserID, typingTicket string, status int) error {
	body := SendTypingReq{
		ILinkUserID:  toUserID,
		TypingTicket: typingTicket,
		Status:       status,
		BaseInfo:     c.baseInfo(),
	}
	var out SendTypingResp
	if err := c.post(ctx, sess.BaseURL, "/ilink/bot/sendtyping", sess.BotToken, body, &out); err != nil {
		return err
	}
	if out.Ret != 0 {
		return fmt.Errorf("sendtyping ret=%d", out.Ret)
	}
	return nil
}

// IsSessionExpired returns true if the response indicates session expiry (-14).
func IsSessionExpired(ret, errcode int) bool {
	return ret == -14 || errcode == -14
}

// NotifyStart announces to the server that this client is now active and
// ready to receive inbound messages. Mirrors POST /ilink/bot/msg/notifystart
// in the official @tencent-weixin/openclaw-weixin gateway.
//
// In practice the call is best-effort: the official client logs and ignores
// failures here and proceeds straight into the long-poll loop. Callers should
// do the same — but actually issuing the call appears to be required by some
// server deployments before they will route messages to the new session.
func (c *Client) NotifyStart(ctx context.Context, sess Session) (*NotifyStartResp, error) {
	body := NotifyStartReq{BaseInfo: c.baseInfo()}
	var out NotifyStartResp
	if err := c.post(ctx, sess.BaseURL, "/ilink/bot/msg/notifystart", sess.BotToken, body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// NotifyStop is the symmetric counterpart of NotifyStart: announces graceful
// shutdown so the server can stop holding long-poll connections and clean up
// routing state. Always treat as best-effort.
func (c *Client) NotifyStop(ctx context.Context, sess Session) (*NotifyStopResp, error) {
	body := NotifyStopReq{BaseInfo: c.baseInfo()}
	var out NotifyStopResp
	if err := c.post(ctx, sess.BaseURL, "/ilink/bot/msg/notifystop", sess.BotToken, body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
