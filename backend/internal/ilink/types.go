package ilink

// BaseInfo is required on every business POST body.
type BaseInfo struct {
	ChannelVersion string `json:"channel_version"`
}

// QRCodeResp is returned by GET /ilink/bot/get_bot_qrcode.
type QRCodeResp struct {
	QRCode         string `json:"qrcode"`
	QRCodeImageURL string `json:"qrcode_img_content"`
}

// QRStatus values.
const (
	QRStatusWait      = "wait"
	QRStatusScanned   = "scaned"
	QRStatusConfirmed = "confirmed"
	QRStatusExpired   = "expired"
)

// QRStatusResp is returned by GET /ilink/bot/get_qrcode_status.
type QRStatusResp struct {
	Status      string `json:"status"`
	BotToken    string `json:"bot_token"`
	ILinkBotID  string `json:"ilink_bot_id"`
	ILinkUserID string `json:"ilink_user_id"`
	BaseURL     string `json:"baseurl"`
}

// Item types.
const (
	ItemTypeText  = 1
	ItemTypeImage = 2
	ItemTypeVoice = 3
	ItemTypeFile  = 4
	ItemTypeVideo = 5
)

// Message types.
const (
	MessageTypeUser = 1
	MessageTypeBot  = 2
)

// Message states.
const (
	MessageStateNew        = 0
	MessageStateGenerating = 1
	MessageStateFinish     = 2
)

// CDNMedia is shared across image/voice/file/video media references.
type CDNMedia struct {
	EncryptQueryParam string `json:"encrypt_query_param,omitempty"`
	AESKey            string `json:"aes_key,omitempty"`
	EncryptType       int    `json:"encrypt_type,omitempty"`
}

type TextItem struct {
	Text string `json:"text"`
}

type ImageItem struct {
	Media       *CDNMedia `json:"media,omitempty"`
	ThumbMedia  *CDNMedia `json:"thumb_media,omitempty"`
	AESKey      string    `json:"aeskey,omitempty"`
	URL         string    `json:"url,omitempty"`
	MidSize     int64     `json:"mid_size,omitempty"`
	ThumbSize   int64     `json:"thumb_size,omitempty"`
	ThumbWidth  int       `json:"thumb_width,omitempty"`
	ThumbHeight int       `json:"thumb_height,omitempty"`
	HDSize      int64     `json:"hd_size,omitempty"`
}

type VoiceItem struct {
	Media         *CDNMedia `json:"media,omitempty"`
	EncodeType    int       `json:"encode_type,omitempty"`
	BitsPerSample int       `json:"bits_per_sample,omitempty"`
	SampleRate    int       `json:"sample_rate,omitempty"`
	Playtime      int64     `json:"playtime,omitempty"`
	Text          string    `json:"text,omitempty"`
}

type FileItem struct {
	Media    *CDNMedia `json:"media,omitempty"`
	FileName string    `json:"file_name,omitempty"`
	MD5      string    `json:"md5,omitempty"`
	Len      string    `json:"len,omitempty"`
}

type VideoItem struct {
	Media       *CDNMedia `json:"media,omitempty"`
	VideoSize   int64     `json:"video_size,omitempty"`
	PlayLength  int64     `json:"play_length,omitempty"`
	VideoMD5    string    `json:"video_md5,omitempty"`
	ThumbMedia  *CDNMedia `json:"thumb_media,omitempty"`
	ThumbSize   int64     `json:"thumb_size,omitempty"`
	ThumbWidth  int       `json:"thumb_width,omitempty"`
	ThumbHeight int       `json:"thumb_height,omitempty"`
}

type MessageItem struct {
	Type         int        `json:"type"`
	CreateTimeMS int64      `json:"create_time_ms,omitempty"`
	UpdateTimeMS int64      `json:"update_time_ms,omitempty"`
	IsCompleted  bool       `json:"is_completed,omitempty"`
	MsgID        string     `json:"msg_id,omitempty"`
	TextItem     *TextItem  `json:"text_item,omitempty"`
	ImageItem    *ImageItem `json:"image_item,omitempty"`
	VoiceItem    *VoiceItem `json:"voice_item,omitempty"`
	FileItem     *FileItem  `json:"file_item,omitempty"`
	VideoItem    *VideoItem `json:"video_item,omitempty"`
}

type WeixinMessage struct {
	Seq       int64 `json:"seq,omitempty"`
	MessageID int64 `json:"message_id,omitempty"`
	// FromUserID is sent as the empty string on outbound sendmessage
	// (matching the official @tencent-weixin/openclaw-weixin client). Inbound
	// messages parsed off the wire fill it with the real WeChat user id;
	// `omitempty` is deliberately absent so an outbound empty value still
	// emits `"from_user_id": ""` on the wire — some server-side deployments
	// reject the request entirely when the key is missing.
	FromUserID   string        `json:"from_user_id"`
	ToUserID     string        `json:"to_user_id,omitempty"`
	ClientID     string        `json:"client_id,omitempty"`
	CreateTimeMS int64         `json:"create_time_ms,omitempty"`
	UpdateTimeMS int64         `json:"update_time_ms,omitempty"`
	DeleteTimeMS int64         `json:"delete_time_ms,omitempty"`
	SessionID    string        `json:"session_id,omitempty"`
	GroupID      string        `json:"group_id,omitempty"`
	MessageType  int           `json:"message_type,omitempty"`
	MessageState int           `json:"message_state,omitempty"`
	ContextToken string        `json:"context_token,omitempty"`
	ItemList     []MessageItem `json:"item_list,omitempty"`
}

// GetUpdatesReq is the body of POST /ilink/bot/getupdates.
type GetUpdatesReq struct {
	GetUpdatesBuf string   `json:"get_updates_buf"`
	BaseInfo      BaseInfo `json:"base_info"`
}

type GetUpdatesResp struct {
	Ret                  int             `json:"ret"`
	ErrCode              int             `json:"errcode,omitempty"`
	ErrMsg               string          `json:"errmsg,omitempty"`
	Msgs                 []WeixinMessage `json:"msgs,omitempty"`
	GetUpdatesBuf        string          `json:"get_updates_buf,omitempty"`
	LongPollingTimeoutMS int             `json:"longpolling_timeout_ms,omitempty"`
}

// SendMessageReq is the body of POST /ilink/bot/sendmessage.
type SendMessageReq struct {
	Msg      WeixinMessage `json:"msg"`
	BaseInfo BaseInfo      `json:"base_info"`
}

type SendMessageResp struct {
	Ret     int    `json:"ret"`
	ErrCode int    `json:"errcode,omitempty"`
	ErrMsg  string `json:"errmsg,omitempty"`
}

// GetConfigReq is the body of POST /ilink/bot/getconfig.
type GetConfigReq struct {
	ILinkUserID  string   `json:"ilink_user_id"`
	ContextToken string   `json:"context_token,omitempty"`
	BaseInfo     BaseInfo `json:"base_info"`
}

type GetConfigResp struct {
	Ret          int    `json:"ret"`
	TypingTicket string `json:"typing_ticket,omitempty"`
}

// SendTypingReq is the body of POST /ilink/bot/sendtyping.
type SendTypingReq struct {
	ILinkUserID  string   `json:"ilink_user_id"`
	TypingTicket string   `json:"typing_ticket"`
	Status       int      `json:"status"` // 1 start, 2 stop
	BaseInfo     BaseInfo `json:"base_info"`
}

type SendTypingResp struct {
	Ret int `json:"ret"`
}

// GetUploadURLReq is the body of POST /ilink/bot/getuploadurl.
type GetUploadURLReq struct {
	FileKey     string   `json:"filekey"`
	MediaType   int      `json:"media_type"`
	ToUserID    string   `json:"to_user_id"`
	RawSize     int64    `json:"rawsize"`
	RawFileMD5  string   `json:"rawfilemd5"`
	FileSize    int64    `json:"filesize"`
	NoNeedThumb bool     `json:"no_need_thumb,omitempty"`
	AESKey      string   `json:"aeskey,omitempty"`
	BaseInfo    BaseInfo `json:"base_info"`
}

type GetUploadURLResp struct {
	Ret              int    `json:"ret"`
	UploadParam      string `json:"upload_param"`
	ThumbUploadParam string `json:"thumb_upload_param"`
}

// NotifyStartReq is the body of POST /ilink/bot/msg/notifystart.
//
// In the official @tencent-weixin/openclaw-weixin plugin the gateway calls
// this when a channel account becomes active (after QR-confirm or on host
// startup) before entering the getupdates long-poll. The server appears to
// treat it as "this bot is now online, route inbound messages to me". Some
// deployments will silently stop pushing messages to a session that never
// announces itself, which manifests as a perpetually-empty getupdates loop
// and an apparently-mute bot. The call is best-effort: failure is logged
// but does not block the long-poll loop.
type NotifyStartReq struct {
	BaseInfo BaseInfo `json:"base_info"`
}

type NotifyStartResp struct {
	Ret    int    `json:"ret"`
	ErrMsg string `json:"errmsg,omitempty"`
}

// NotifyStopReq is the body of POST /ilink/bot/msg/notifystop.
//
// Symmetric counterpart of NotifyStartReq. Sent on graceful shutdown so the
// server knows to stop holding long-poll connections / re-route inbound.
type NotifyStopReq struct {
	BaseInfo BaseInfo `json:"base_info"`
}

type NotifyStopResp struct {
	Ret    int    `json:"ret"`
	ErrMsg string `json:"errmsg,omitempty"`
}
