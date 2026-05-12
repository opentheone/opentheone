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
	"time"
)

// Client talks the iLink ClawBot HTTP/JSON protocol.
type Client struct {
	httpClient     *http.Client
	BaseURL        string
	CDNBaseURL     string
	ChannelVersion string
	UserAgent      string
	SKRouteTag     string
}

// ClientOptions configures Client.
type ClientOptions struct {
	BaseURL         string
	CDNBaseURL      string
	ChannelVersion  string
	UserAgent       string
	SKRouteTag      string
	LongPollTimeout time.Duration
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
		opts.LongPollTimeout = 40 * time.Second
	}

	httpClient := &http.Client{Timeout: opts.LongPollTimeout + 5*time.Second}
	return &Client{
		httpClient:     httpClient,
		BaseURL:        opts.BaseURL,
		CDNBaseURL:     opts.CDNBaseURL,
		ChannelVersion: opts.ChannelVersion,
		UserAgent:      opts.UserAgent,
		SKRouteTag:     opts.SKRouteTag,
	}
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

// applyAuthHeaders sets the four required headers for an authenticated POST.
func (c *Client) applyAuthHeaders(req *http.Request, botToken string) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("AuthorizationType", "ilink_bot_token")
	req.Header.Set("Authorization", "Bearer "+botToken)
	req.Header.Set("X-WECHAT-UIN", randomWechatUIN())
	if c.SKRouteTag != "" {
		req.Header.Set("SKRouteTag", c.SKRouteTag)
	}
	req.Header.Set("User-Agent", c.UserAgent)
}

// GetBotQRCode requests a fresh QR code (no auth required).
func (c *Client) GetBotQRCode(ctx context.Context) (*QRCodeResp, error) {
	u := c.BaseURL + "/ilink/bot/get_bot_qrcode?bot_type=3"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	if c.SKRouteTag != "" {
		req.Header.Set("SKRouteTag", c.SKRouteTag)
	}
	req.Header.Set("User-Agent", c.UserAgent)

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
	req.Header.Set("iLink-App-ClientVersion", "1")
	if c.SKRouteTag != "" {
		req.Header.Set("SKRouteTag", c.SKRouteTag)
	}
	req.Header.Set("User-Agent", c.UserAgent)

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
