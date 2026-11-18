package main

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

const attachRetention = time.Hour

func attachCacheDir() (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("locate attachment cache: %w", err)
	}
	dir = filepath.Join(dir, "wagon", "attachment-uploads")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create attachment cache: %w", err)
	}
	return dir, nil
}

// initReceipt keys the cache by rail identity, request, file position and
// content. The certificate issuer and CN survive normal certificate rotation.
func (f *attachFile) initReceipt(c *client, dir, requestID string, index int) error {
	src, err := os.Open(f.path)
	if err != nil {
		return fmt.Errorf("open attachment %s: %w", f.path, err)
	}
	defer func() { _ = src.Close() }()
	sum := sha256.New()
	if _, err := io.Copy(sum, src); err != nil {
		return fmt.Errorf("hash attachment %s: %w", f.path, err)
	}
	f.digest = sum.Sum(nil)
	receiptKey := fmt.Sprintf("%q %x %x %q %q %d %q %x", c.base, c.leaf.RawIssuer, c.leaf.AuthorityKeyId,
		c.leaf.Subject.CommonName, requestID, index, filepath.Base(f.path), f.digest)
	f.receipt = filepath.Join(dir, fmt.Sprintf("%x", sha256.Sum256([]byte(receiptKey))))
	return f.readReceipt()
}

func (f *attachFile) readReceipt() error {
	r, err := os.Open(f.receipt)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read attachment receipt: %w", err)
	}
	defer func() { _ = r.Close() }()
	info, err := r.Stat()
	if err != nil {
		return fmt.Errorf("stat attachment receipt: %w", err)
	}
	value, err := io.ReadAll(io.LimitReader(r, 65))
	if err != nil {
		return fmt.Errorf("read attachment receipt: %w", err)
	}
	if len(value) > 0 && len(value) <= 64 {
		f.id = string(value)
		f.uploadedAt = info.ModTime()
	}
	return nil
}

func (f *attachFile) saveReceipt(id string) error {
	if f.receipt == "" {
		return nil
	}
	// Publish one complete receipt even if two processes use the same request ID.
	tmp, err := os.CreateTemp(filepath.Dir(f.receipt), ".upload-*")
	if err != nil {
		return fmt.Errorf("create attachment receipt: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	_, writeErr := io.WriteString(tmp, id)
	if err := errors.Join(writeErr, tmp.Close()); err != nil {
		return fmt.Errorf("write attachment receipt: %w", err)
	}
	if err := os.Rename(tmp.Name(), f.receipt); err != nil {
		return fmt.Errorf("save attachment receipt: %w", err)
	}
	return nil
}

func clearExpiredReceipts(files []attachFile, now time.Time) bool {
	cleared := false
	for i := range files {
		if !files[i].uploadedAt.IsZero() && now.Sub(files[i].uploadedAt) >= attachRetention {
			files[i].id = ""
			files[i].uploadedAt = time.Time{}
			cleared = true
		}
	}
	return cleared
}
