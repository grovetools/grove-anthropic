package anthropic

import (
	"context"
	"fmt"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/mattsolo1/grove-anthropic/pkg/config"
)

// DefaultModel is the default Claude model to use if none is specified.
const DefaultModel = "claude-sonnet-4-20250514"

// Client wraps the official Anthropic client.
type Client struct {
	client anthropic.Client
}

// NewClient creates a new Anthropic client wrapper.
func NewClient(apiKeyOverride string) (*Client, error) {
	var apiKey string
	var err error

	if apiKeyOverride != "" {
		apiKey = apiKeyOverride
	} else {
		apiKey, err = config.ResolveAPIKey()
		if err != nil {
			return nil, err
		}
	}

	sdkClient := anthropic.NewClient(option.WithAPIKey(apiKey))
	return &Client{client: sdkClient}, nil
}

// GenerateContent sends a request to the Anthropic API with context files.
func (c *Client) GenerateContent(ctx context.Context, model, prompt, systemPrompt string, contextFiles []string, maxTokens int64) (string, *anthropic.BetaUsage, error) {
	var contentBlocks []anthropic.BetaContentBlockParamUnion

	// 1. Add the text prompt
	contentBlocks = append(contentBlocks, anthropic.NewBetaTextBlock(prompt))

	// 2. Upload context files and add them as document blocks
	for _, filePath := range contextFiles {
		metadata, err := uploadFile(ctx, &c.client, filePath)
		if err != nil {
			return "", nil, fmt.Errorf("uploading context file %s: %w", filePath, err)
		}

		contentBlocks = append(contentBlocks, anthropic.NewBetaDocumentBlock(anthropic.BetaFileDocumentSourceParam{
			FileID: metadata.ID,
		}))
	}

	// 3. Construct the message parameters
	params := anthropic.BetaMessageNewParams{
		Model:     anthropic.Model(model),
		MaxTokens: maxTokens,
		Messages: []anthropic.BetaMessageParam{
			anthropic.NewBetaUserMessage(contentBlocks...),
		},
		Betas: []anthropic.AnthropicBeta{anthropic.AnthropicBetaFilesAPI2025_04_14},
	}

	// 4. Add system prompt if provided
	if systemPrompt != "" {
		params.System = []anthropic.BetaTextBlockParam{{
			Type: "text",
			Text: systemPrompt,
		}}
	}

	// 5. Make the API call
	resp, err := c.client.Beta.Messages.New(ctx, params)
	if err != nil {
		return "", nil, fmt.Errorf("anthropic API request failed: %w", err)
	}

	// 6. Extract the response text from all text blocks
	var textParts []string
	for _, block := range resp.Content {
		if block.Type == "text" {
			textParts = append(textParts, block.Text)
		}
	}
	responseText := strings.Join(textParts, "")

	if responseText == "" {
		return "", &resp.Usage, fmt.Errorf("no text content in response")
	}

	return responseText, &resp.Usage, nil
}
