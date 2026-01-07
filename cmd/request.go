package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/mattsolo1/grove-anthropic/pkg/anthropic"
	"github.com/mattsolo1/grove-anthropic/pkg/pretty"
	"github.com/spf13/cobra"
)

var (
	requestModel        string
	requestPrompt       string
	requestSystemPrompt string
	requestPromptFile   string
	requestWorkDir      string
	requestOutputFile   string
	requestContextFiles []string
	requestRegenerate   bool
	requestMaxTokens    int64
)

func newRequestCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "request",
		Short: "Make a request to the Anthropic API",
		Long:  `Make a request to the Anthropic API using grove-context for automatic context management.`,
		RunE:  runRequest,
	}

	cmd.Flags().StringVarP(&requestModel, "model", "m", anthropic.DefaultModel, "Anthropic model to use")
	cmd.Flags().StringVarP(&requestPrompt, "prompt", "p", "", "Prompt text")
	cmd.Flags().StringVarP(&requestPromptFile, "file", "f", "", "Read prompt from file")
	cmd.Flags().StringVarP(&requestSystemPrompt, "system", "s", "", "System prompt text")
	cmd.Flags().StringVarP(&requestWorkDir, "workdir", "w", "", "Working directory (defaults to current)")
	cmd.Flags().StringVarP(&requestOutputFile, "output", "o", "", "Write response to file instead of stdout")
	cmd.Flags().StringSliceVar(&requestContextFiles, "context", nil, "Additional context files to include")
	cmd.Flags().BoolVar(&requestRegenerate, "regenerate", false, "Regenerate context before request")
	cmd.Flags().Int64Var(&requestMaxTokens, "max-tokens", 8192, "Maximum tokens in response")

	return cmd
}

func runRequest(cmd *cobra.Command, args []string) error {
	ctx := context.Background()

	if requestPrompt == "" && requestPromptFile == "" && len(args) == 0 {
		return fmt.Errorf("must provide prompt via -p, -f, or as an argument")
	}

	var promptText string
	if requestPrompt != "" {
		promptText = requestPrompt
	} else if requestPromptFile != "" {
		content, err := os.ReadFile(requestPromptFile)
		if err != nil {
			return fmt.Errorf("reading prompt file: %w", err)
		}
		promptText = string(content)
	} else if len(args) > 0 {
		promptText = strings.Join(args, " ")
	}

	options := anthropic.RequestOptions{
		Model:         requestModel,
		Prompt:        promptText,
		SystemPrompt:  requestSystemPrompt,
		WorkDir:       requestWorkDir,
		ContextFiles:  requestContextFiles,
		RegenerateCtx: requestRegenerate,
		MaxTokens:     requestMaxTokens,
	}

	runner := anthropic.NewRequestRunner()
	response, err := runner.Run(ctx, options)
	if err != nil {
		return err
	}

	if requestOutputFile != "" {
		if err := os.WriteFile(requestOutputFile, []byte(response), 0644); err != nil {
			return fmt.Errorf("writing output file: %w", err)
		}
		logger := pretty.New()
		logger.ResponseWritten(requestOutputFile)
	} else {
		fmt.Print(response)
		if !strings.HasSuffix(response, "\n") {
			fmt.Println()
		}
	}

	return nil
}
