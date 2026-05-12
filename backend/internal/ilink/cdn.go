package ilink

import (
	"context"
	"crypto/aes"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// DecodeAESKey accepts the three encodings observed in the wild and returns the raw 16-byte key:
//   - base64( 16 raw bytes ) → length 24 base64 (e.g. "ABEiM0RV...")
//   - base64( 32 ASCII hex chars ) → length 44 base64 (e.g. "MDAxMTIyMzM...")
//   - 32 ASCII hex chars (no base64) — used by image_item.aeskey
func DecodeAESKey(s string) ([]byte, error) {
	if s == "" {
		return nil, errors.New("empty aes key")
	}
	if len(s) == 32 {
		if raw, err := hex.DecodeString(s); err == nil && len(raw) == 16 {
			return raw, nil
		}
	}
	dec, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("decode aes key: %w", err)
	}
	switch len(dec) {
	case 16:
		return dec, nil
	case 32:
		raw, err := hex.DecodeString(string(dec))
		if err != nil {
			return nil, fmt.Errorf("hex inside base64: %w", err)
		}
		if len(raw) != 16 {
			return nil, fmt.Errorf("unexpected key length: %d", len(raw))
		}
		return raw, nil
	default:
		return nil, fmt.Errorf("unexpected aes key payload length: %d", len(dec))
	}
}

// decryptAESECB performs AES-128-ECB + PKCS7 decryption.
func decryptAESECB(ciphertext, key []byte) ([]byte, error) {
	if len(key) != 16 {
		return nil, fmt.Errorf("key must be 16 bytes, got %d", len(key))
	}
	if len(ciphertext)%aes.BlockSize != 0 {
		return nil, fmt.Errorf("ciphertext not a multiple of block size: %d", len(ciphertext))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	plaintext := make([]byte, len(ciphertext))
	for i := 0; i < len(ciphertext); i += aes.BlockSize {
		block.Decrypt(plaintext[i:i+aes.BlockSize], ciphertext[i:i+aes.BlockSize])
	}
	return pkcs7Unpad(plaintext, aes.BlockSize)
}

func pkcs7Unpad(data []byte, blockSize int) ([]byte, error) {
	if len(data) == 0 || len(data)%blockSize != 0 {
		return data, nil
	}
	pad := int(data[len(data)-1])
	if pad <= 0 || pad > blockSize {
		return data, nil
	}
	for i := len(data) - pad; i < len(data); i++ {
		if int(data[i]) != pad {
			return data, nil
		}
	}
	return data[:len(data)-pad], nil
}

// DownloadAndDecrypt fetches an encrypted media blob from the CDN and decrypts it.
// `encryptedQueryParam` is the value of `CDNMedia.EncryptQueryParam` from the inbound message.
// `aesKeyEncoded` is the value of either `CDNMedia.AESKey` or `image_item.aeskey`.
// Returns the plaintext payload.
func (c *Client) DownloadAndDecrypt(ctx context.Context, encryptedQueryParam, aesKeyEncoded string) ([]byte, error) {
	if encryptedQueryParam == "" {
		return nil, errors.New("missing encrypt_query_param")
	}
	key, err := DecodeAESKey(aesKeyEncoded)
	if err != nil {
		return nil, err
	}

	u := c.CDNBaseURL + "/download?encrypted_query_param=" + url.QueryEscape(encryptedQueryParam)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.UserAgent)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("cdn download http %d: %s", resp.StatusCode, string(body))
	}
	// Cap reads so a hostile or buggy CDN response can't OOM the process.
	// WeChat hard-caps user-sendable media well below this (≈100MB for files,
	// much less for images/voice); anything bigger is almost certainly an
	// attack or a misconfigured endpoint.
	const maxCDNBytes = 50 * 1024 * 1024
	ciphertext, err := io.ReadAll(io.LimitReader(resp.Body, maxCDNBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(ciphertext)) > maxCDNBytes {
		return nil, fmt.Errorf("cdn payload exceeds %d byte cap", maxCDNBytes)
	}
	if len(ciphertext) == 0 {
		return nil, errors.New("cdn returned empty body")
	}
	return decryptAESECB(ciphertext, key)
}

// ChooseImageAESKey returns the best AES key for an image — image_item.aeskey is hex and is
// preferred when set, falling back to media.aes_key.
func ChooseImageAESKey(img *ImageItem) string {
	if img == nil {
		return ""
	}
	if img.AESKey != "" {
		return img.AESKey
	}
	if img.Media != nil {
		return img.Media.AESKey
	}
	return ""
}
