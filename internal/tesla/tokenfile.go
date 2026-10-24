package tesla

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// WriteRefreshTokenFile atomically replaces the refresh token file with an
// owner-only file, so a crash never leaves a truncated token behind.
func WriteRefreshTokenFile(path, refreshToken string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("token path is required")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create token directory: %w", err)
	}

	tmpFile, err := os.CreateTemp(dir, "refresh-token-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp token file: %w", err)
	}
	tmpPath := tmpFile.Name()

	defer func() {
		_ = tmpFile.Close()
		_ = os.Remove(tmpPath)
	}()

	if err := tmpFile.Chmod(0o600); err != nil {
		return fmt.Errorf("chmod temp token file: %w", err)
	}
	if _, err := tmpFile.WriteString(strings.TrimSpace(refreshToken)); err != nil {
		return fmt.Errorf("write temp token file: %w", err)
	}
	if err := tmpFile.Sync(); err != nil {
		return fmt.Errorf("sync temp token file: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("close temp token file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replace token file: %w", err)
	}

	return nil
}

// ResolveRefreshToken picks the startup refresh token. The token file wins
// over the configured (environment) token because Tesla rotates refresh
// tokens: the file holds the newest one, while the environment value goes
// stale after the first rotation. It reports which source was used.
func ResolveRefreshToken(path, configured string) (token, source string, err error) {
	if strings.TrimSpace(path) != "" {
		data, readErr := os.ReadFile(path)
		switch {
		case readErr == nil:
			if fileToken := strings.TrimSpace(string(data)); fileToken != "" {
				return fileToken, "file", nil
			}
		case !errors.Is(readErr, os.ErrNotExist):
			err = fmt.Errorf("read refresh token file: %w", readErr)
		}
	}
	if configured = strings.TrimSpace(configured); configured != "" {
		return configured, "environment", err
	}
	return "", "", err
}
