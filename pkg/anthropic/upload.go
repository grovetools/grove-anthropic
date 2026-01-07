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
	file, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open file %s: %w", filePath, err)
	}
	defer file.Close()

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
func detectMIMEType(filePath string) string {
	ext := strings.ToLower(filepath.Ext(filePath))
	switch ext {
	case ".txt", ".text":
		return "text/plain"
	case ".md", ".markdown":
		return "text/markdown"
	case ".json":
		return "application/json"
	case ".go":
		return "text/x-go"
	case ".py":
		return "text/x-python"
	case ".js":
		return "text/javascript"
	case ".ts":
		return "text/typescript"
	case ".html", ".htm":
		return "text/html"
	case ".css":
		return "text/css"
	case ".xml":
		return "text/xml"
	case ".yaml", ".yml":
		return "text/yaml"
	case ".sh":
		return "text/x-shellscript"
	case ".rs":
		return "text/x-rust"
	case ".java":
		return "text/x-java"
	case ".c":
		return "text/x-c"
	case ".cpp", ".cc", ".cxx":
		return "text/x-c++"
	case ".h", ".hpp":
		return "text/x-c"
	case ".rb":
		return "text/x-ruby"
	case ".php":
		return "text/x-php"
	case ".swift":
		return "text/x-swift"
	case ".kt", ".kts":
		return "text/x-kotlin"
	case ".scala":
		return "text/x-scala"
	default:
		return "text/plain"
	}
}
