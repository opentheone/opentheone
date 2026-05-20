package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"

	"go.uber.org/zap"

	"github.com/opentheone/opentheone/backend/internal/ilink"
	"github.com/opentheone/opentheone/backend/internal/model"
)

// downloadIfNeeded best-effort downloads & decrypts inbound media for one MessageItem.
// On success it creates an Attachment row pointing at a file inside attachmentsDir and
// updates the message's MediaURL to that local path.
func (e *Engine) downloadIfNeeded(ctx context.Context, msgID string, item *ilink.MessageItem) {
	if e.attachmentsDir == "" {
		return
	}
	var (
		kind         string
		encryptParam string
		aesKey       string
		filenameHint string
		expectedMime string
	)
	switch item.Type {
	case ilink.ItemTypeImage:
		kind = "image"
		if item.ImageItem == nil {
			return
		}
		if item.ImageItem.Media == nil {
			return
		}
		encryptParam = item.ImageItem.Media.EncryptQueryParam
		aesKey = ilink.ChooseImageAESKey(item.ImageItem)
		expectedMime = "image/jpeg"
		filenameHint = "image.jpg"
	case ilink.ItemTypeFile:
		kind = "file"
		if item.FileItem == nil || item.FileItem.Media == nil {
			return
		}
		encryptParam = item.FileItem.Media.EncryptQueryParam
		aesKey = item.FileItem.Media.AESKey
		filenameHint = item.FileItem.FileName
		if filenameHint == "" {
			filenameHint = "file"
		}
		// File mime is unknowable without sniffing; we leave it on the
		// attachment row so the HTTP handler can fall back to the catch-all
		// "application/octet-stream" rather than a wrong guess. The
		// filename extension is preserved on disk for clients that can
		// dispatch by extension.
		expectedMime = "application/octet-stream"
	case ilink.ItemTypeVoice:
		kind = "voice"
		if item.VoiceItem == nil || item.VoiceItem.Media == nil {
			return
		}
		encryptParam = item.VoiceItem.Media.EncryptQueryParam
		aesKey = item.VoiceItem.Media.AESKey
		filenameHint = "voice.silk"
		// WeChat voice notes are SILK-encoded. No registered IANA mime; the
		// de-facto string in the WeChat ecosystem is "audio/silk". Browsers
		// won't play it natively, but at least the download dialog shows a
		// sensible type and command-line tooling (ffmpeg, silk_v3_decoder)
		// can pick it up.
		expectedMime = "audio/silk"
	case ilink.ItemTypeVideo:
		// Without a video branch the engine used to silently skip every
		// inbound video — the conversation showed "[video]" but the bytes
		// never landed on disk and /api/attachment/get returned 404. The
		// VideoItem schema mirrors ImageItem/FileItem with an outer
		// CDNMedia, so the same decrypt path works.
		kind = "video"
		if item.VideoItem == nil || item.VideoItem.Media == nil {
			return
		}
		encryptParam = item.VideoItem.Media.EncryptQueryParam
		aesKey = item.VideoItem.Media.AESKey
		filenameHint = "video.mp4"
		expectedMime = "video/mp4"
	default:
		return
	}
	if encryptParam == "" || aesKey == "" {
		return
	}

	payload, err := e.ilink.DownloadAndDecrypt(ctx, encryptParam, aesKey)
	if err != nil {
		e.log.Debug("cdn download failed",
			zap.String("kind", kind),
			zap.Error(err))
		return
	}

	sum := sha256.Sum256(payload)
	// filepath.Ext already returns "" when there is no extension, so we can
	// safely concatenate unconditionally.
	localName := hex.EncodeToString(sum[:]) + filepath.Ext(filenameHint)
	localPath := filepath.Join(e.attachmentsDir, localName)
	if err := os.WriteFile(localPath, payload, 0o600); err != nil {
		e.log.Debug("write attachment failed", zap.Error(err))
		return
	}

	att := model.Attachment{
		MessageID: msgID,
		Kind:      kind,
		LocalPath: localPath,
		Size:      int64(len(payload)),
		Mime:      expectedMime,
	}
	if err := e.db.WithContext(ctx).Create(&att).Error; err != nil {
		e.log.Debug("persist attachment failed", zap.Error(err))
		return
	}
	relPath := localPath
	if cwd, err := os.Getwd(); err == nil {
		if rel, err := filepath.Rel(cwd, localPath); err == nil && !strings.HasPrefix(rel, "..") {
			relPath = rel
		}
	}
	_ = e.db.WithContext(ctx).Model(&model.Message{}).
		Where("id = ?", msgID).
		Update("media_url", relPath).Error
}
