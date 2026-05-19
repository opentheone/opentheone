package handler

import (
	"encoding/base64"
	"net/http"
	"os"
	"path/filepath"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/opentheone/opentheone/backend/internal/model"
)

type AttachmentHandler struct {
	db *gorm.DB
}

func NewAttachmentHandler(db *gorm.DB) *AttachmentHandler {
	return &AttachmentHandler{db: db}
}

type attachmentGetReq struct {
	AttachmentID string `json:"attachment_id"`
	MessageID    string `json:"message_id"`
}

// Get returns a single attachment encoded as base64 along with mime + filename.
// Caller may identify by attachment_id or by message_id (returns the first attachment).
func (h *AttachmentHandler) Get(c *gin.Context) {
	uid := currentUserID(c)
	var req attachmentGetReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, 400, "invalid json")
		return
	}
	if req.AttachmentID == "" && req.MessageID == "" {
		fail(c, http.StatusBadRequest, 400, "attachment_id or message_id required")
		return
	}

	var att model.Attachment
	q := h.db.Model(&model.Attachment{})
	if req.AttachmentID != "" {
		q = q.Where("id = ?", req.AttachmentID)
	} else {
		q = q.Where("message_id = ?", req.MessageID)
	}
	if err := q.First(&att).Error; err != nil {
		fail(c, http.StatusNotFound, 404, "not found")
		return
	}

	var count int64
	h.db.Raw(`SELECT count(1) FROM messages m
		JOIN conversations c ON m.conversation_id = c.id
		JOIN we_chat_bindings b ON c.binding_id = b.id
		WHERE m.id = ? AND b.user_id = ?`, att.MessageID, uid).Scan(&count)
	if count == 0 {
		fail(c, http.StatusNotFound, 404, "not found")
		return
	}

	if att.LocalPath == "" {
		fail(c, http.StatusNotFound, 404, "no local file")
		return
	}
	data, err := os.ReadFile(att.LocalPath)
	if err != nil {
		fail(c, http.StatusInternalServerError, 500, err.Error())
		return
	}

	const maxBytes = 8 * 1024 * 1024
	if len(data) > maxBytes {
		fail(c, http.StatusRequestEntityTooLarge, 413, "file too large to inline")
		return
	}

	mime := att.Mime
	if mime == "" {
		mime = "application/octet-stream"
	}
	ok(c, gin.H{
		"attachment_id": att.ID,
		"message_id":    att.MessageID,
		"kind":          att.Kind,
		"mime":          mime,
		"size":          att.Size,
		"filename":      filepath.Base(att.LocalPath),
		"data_base64":   base64.StdEncoding.EncodeToString(data),
	})
}
