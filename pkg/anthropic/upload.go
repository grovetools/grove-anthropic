package anthropic

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
)

// uploadFile uploads a single file to the Anthropic API and returns its metadata.
func uploadFile(ctx context.Context, client *anthropic.Client, filePath string) (*anthropic.FileMetadata, error) {
	file, err := os.Open(filePath) //nolint:gosec // filePath is caller-provided context file, not user input
	if err != nil {
		return nil, fmt.Errorf("failed to open file %s: %w", filePath, err)
	}
	defer func() { _ = file.Close() }()

	fileReader := anthropic.File(file, filepath.Base(filePath), detectMIMEType(filePath))

	metadata, err := client.Beta.Files.Upload(ctx, anthropic.BetaFileUploadParams{
		File:  fileReader,
		Betas: []anthropic.AnthropicBeta{anthropic.AnthropicBetaFilesAPI2025_04_14},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to upload file %s: %w", filePath, err)
	}

	return metadata, nil
}

// detectMIMEType returns an appropriate MIME type for a file based on its extension.
// Note: Anthropic's Files API only supports "text/plain" and "application/pdf".
// All text-based files are uploaded as text/plain.
func detectMIMEType(filePath string) string {
	ext := strings.ToLower(filepath.Ext(filePath))
	switch ext {
	case ".pdf":
		return "application/pdf"
	default:
		// All other files are treated as plaintext
		// The Files API doesn't support specialized MIME types like text/markdown,
		// text/x-go, application/json, etc.
		return "text/plain"
	}
}
