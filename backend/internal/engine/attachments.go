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
	case ilink.ItemTypeVoice:
		kind = "voice"
		if item.VoiceItem == nil || item.VoiceItem.Media == nil {
			return
		}
		encryptParam = item.VoiceItem.Media.EncryptQueryParam
		aesKey = item.VoiceItem.Media.AESKey
		filenameHint = "voice.silk"
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
	ext := filepath.Ext(filenameHint)
	if ext == "" {
		ext = ""
	}
	localName := hex.EncodeToString(sum[:]) + ext
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
