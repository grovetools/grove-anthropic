package pretty

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	corelogging "github.com/grovetools/core/logging"
	"github.com/grovetools/core/tui/theme"
)

// Logger is a wrapper around the grove-core UnifiedLogger with Anthropic-specific helpers.
type Logger struct {
	*corelogging.PrettyLogger
	ulog   *corelogging.UnifiedLogger
	writer io.Writer
	theme  *theme.Theme
}

// TokenFields represents token usage metrics with verbosity levels
type TokenFields struct {
	InputTokens      int     `json:"input_tokens" verbosity:"0"`
	OutputTokens     int     `json:"output_tokens" verbosity:"0"`
	TotalTokens      int     `json:"total_tokens" verbosity:"0"`
	ResponseTimeMs   int64   `json:"response_time_ms" verbosity:"0"`
	EstimatedCostUSD float64 `json:"estimated_cost_usd" verbosity:"0"`
}

// ModelFields represents model information with verbosity level
type ModelFields struct {
	Model string `json:"model" verbosity:"3"`
}

// New creates a new Anthropic-specific pretty logger.
func New() *Logger {
	return &Logger{
		PrettyLogger: corelogging.NewPrettyLogger(),
		ulog:         corelogging.NewUnifiedLogger("grove-anthropic"),
		writer:       corelogging.GetGlobalOutput(),
		theme:        theme.DefaultTheme,
	}
}

// NewWithWriter creates a new Logger with a custom writer
func NewWithWriter(w io.Writer) *Logger {
	return &Logger{
		PrettyLogger: corelogging.NewPrettyLogger().WithWriter(w),
		ulog:         corelogging.NewUnifiedLogger("grove-anthropic"),
		writer:       w,
		theme:        theme.DefaultTheme,
	}
}

// WorkingDirectoryCtx logs the working directory to the writer from the context
func (l *Logger) WorkingDirectoryCtx(ctx context.Context, dir string) {
	pathStyle := lipgloss.NewStyle().Italic(true)
	l.ulog.Info("Working directory").
		Field("directory", dir).
		Pretty(fmt.Sprintf("%s Working directory: %s", theme.IconHome, pathStyle.Render(dir))).
		Log(ctx)
}

// WorkingDirectory logs the working directory
func (l *Logger) WorkingDirectory(dir string) {
	l.WorkingDirectoryCtx(context.Background(), dir)
}

// FoundRulesFileCtx logs that a rules file was found to the writer from the context
func (l *Logger) FoundRulesFileCtx(ctx context.Context, path string) {
	l.PathCtx(ctx, theme.IconChecklist+" Found rules file", path)
}

// FoundRulesFile logs that a rules file was found
func (l *Logger) FoundRulesFile(path string) {
	l.FoundRulesFileCtx(context.Background(), path)
}

// WarningCtx logs a warning message to the writer from the context
func (l *Logger) WarningCtx(ctx context.Context, message string) {
	l.WarnPrettyCtx(ctx, message)
}

// Warning logs a warning message
func (l *Logger) Warning(message string) {
	l.WarningCtx(context.Background(), message)
}

// InfoCtx logs an info message to the writer from the context
func (l *Logger) InfoCtx(ctx context.Context, message string) {
	l.InfoPrettyCtx(ctx, message)
}

// Info logs an info message
func (l *Logger) Info(message string) {
	l.InfoCtx(context.Background(), message)
}

// SuccessCtx logs a success message to the writer from the context
func (l *Logger) SuccessCtx(ctx context.Context, message string) {
	l.PrettyLogger.SuccessCtx(ctx, message)
}

// Success logs a success message
func (l *Logger) Success(message string) {
	l.SuccessCtx(context.Background(), message)
}

// Error logs an error message
func (l *Logger) Error(message string) {
	l.ulog.Error(message).Emit()
}

// ModelCtx logs the model being used to the writer from the context
func (l *Logger) ModelCtx(ctx context.Context, model string) {
	l.ulog.Info("Calling Anthropic API").
		Field("model", model).
		Pretty(fmt.Sprintf("%s Calling Anthropic API with model: %s", theme.IconRobot, model)).
		Log(ctx)
}

// Model logs the model being used
func (l *Logger) Model(model string) {
	l.ModelCtx(context.Background(), model)
}

// UploadProgressCtx logs file upload progress to the writer from the context
func (l *Logger) UploadProgressCtx(ctx context.Context, message string) {
	l.ulog.Progress(message).Log(ctx)
}

// UploadProgress logs file upload progress
func (l *Logger) UploadProgress(message string) {
	l.UploadProgressCtx(context.Background(), message)
}

// UploadComplete logs successful file upload
func (l *Logger) UploadComplete(filename string, duration time.Duration) {
	l.ulog.Success("Upload complete").
		Field("filename", filename).
		Field("duration_seconds", duration.Seconds()).
		Pretty(fmt.Sprintf("%s %s %s",
			l.theme.Success.Render(theme.IconSuccess),
			l.theme.Success.Render(filename),
			l.theme.Muted.Render(fmt.Sprintf("(%.2fs)", duration.Seconds())))).
		Emit()
}

// FilesIncludedCtx displays the list of files that will be included in the request
func (l *Logger) FilesIncludedCtx(ctx context.Context, files []string) {
	if len(files) == 0 {
		return
	}

	pathStyle := lipgloss.NewStyle().Italic(true)
	var prettyLines []string
	prettyLines = append(prettyLines, fmt.Sprintf("%s Files attached to request:", theme.IconFile))

	for _, file := range files {
		displayName := file
		if idx := strings.LastIndex(file, "/"); idx != -1 {
			displayName = file[idx+1:]
		}
		if displayName == "CLAUDE.md" || displayName == "context" || displayName == "cached-context" {
			prettyLines = append(prettyLines, fmt.Sprintf("%s %s", l.theme.Highlight.Render(theme.IconBullet), pathStyle.Render(file)))
		} else {
			prettyLines = append(prettyLines, fmt.Sprintf("%s %s", l.theme.Highlight.Render(theme.IconBullet), pathStyle.Render(displayName)))
		}
	}

	l.ulog.Info("Files attached to request").
		Field("files", files).
		Field("count", len(files)).
		Pretty(strings.Join(prettyLines, "\n")).
		Log(ctx)
}

// FilesIncluded displays the list of files that will be included in the request
func (l *Logger) FilesIncluded(files []string) {
	l.FilesIncludedCtx(context.Background(), files)
}

// TokenUsageCtx displays token usage statistics in a styled box
func (l *Logger) TokenUsageCtx(ctx context.Context, inputTokens, outputTokens int, responseTime time.Duration, estimatedCost float64) {
	totalTokens := inputTokens + outputTokens

	divider := l.theme.Muted.Render(strings.Repeat("-", 32))
	content := []string{
		fmt.Sprintf("%s %s",
			l.theme.Muted.Render("Input Tokens:"),
			l.theme.Normal.Render(fmt.Sprintf("%d", inputTokens))),
		fmt.Sprintf("%s %s",
			l.theme.Muted.Render("Output Tokens:"),
			l.theme.Normal.Render(fmt.Sprintf("%d", outputTokens))),
		divider,
		fmt.Sprintf("%s %s",
			l.theme.Muted.Render("Total Tokens:"),
			l.theme.Normal.Render(fmt.Sprintf("%d", totalTokens))),
		fmt.Sprintf("%s %s",
			l.theme.Muted.Render("Estimated Cost:"),
			l.theme.Success.Render(fmt.Sprintf("$%.4f", estimatedCost))),
		fmt.Sprintf("%s %s",
			l.theme.Muted.Render("Response Time:"),
			l.theme.Muted.Render(fmt.Sprintf("%.2fs", responseTime.Seconds()))),
	}

	tokenBox := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(l.theme.Colors.Violet).
		Padding(0, 1)

	box := tokenBox.Render(strings.Join(content, "\n"))

	l.ulog.Info("Anthropic Response & Token Summary").
		Field("input_tokens", inputTokens).
		Field("output_tokens", outputTokens).
		Field("total_tokens", totalTokens).
		Field("response_time_ms", responseTime.Milliseconds()).
		Field("estimated_cost_usd", estimatedCost).
		Pretty(fmt.Sprintf("%s Token usage:\n%s", theme.IconChart, box)).
		Log(ctx)
}

// TokenUsage displays token usage statistics in a styled box
func (l *Logger) TokenUsage(inputTokens, outputTokens int, responseTime time.Duration, estimatedCost float64) {
	l.TokenUsageCtx(context.Background(), inputTokens, outputTokens, responseTime, estimatedCost)
}

// ResponseWritten logs successful response write
func (l *Logger) ResponseWritten(path string) {
	pathStyle := lipgloss.NewStyle().Foreground(theme.Cyan).Italic(true)
	l.ulog.Success("Response written").
		Field("path", path).
		Pretty(fmt.Sprintf("%s %s %s",
			l.theme.Success.Render(theme.IconSuccess),
			l.theme.Success.Render("Response written to:"),
			pathStyle.Render(path))).
		Emit()
}

// Tip logs a helpful tip
func (l *Logger) Tip(message string) {
	l.ulog.Info(message).
		Icon(theme.IconLightbulb).
		Pretty(l.theme.Info.Render(theme.IconLightbulb + " " + message)).
		Emit()
}

// Progress logs a progress message
func (l *Logger) Progress(message string) {
	l.ulog.Progress(message).NoIcon().Pretty(message).Emit()
}

// Blank prints a blank line
func (l *Logger) Blank() {
	// Keep fmt for blank lines - ulog would add unwanted structure
	fmt.Fprintln(l.writer)
}

// Section prints a section header
func (l *Logger) Section(title string) {
	l.ulog.Info(title).
		Pretty(l.theme.Header.Render(title)).
		PrettyOnly().
		Emit()
}

// Field prints a labeled field
func (l *Logger) Field(label string, value interface{}) {
	l.ulog.Info(label).
		Field(label, value).
		Pretty(fmt.Sprintf("%s %s %v",
			l.theme.Highlight.Render(theme.IconBullet),
			l.theme.Muted.Render(label+":"),
			value)).
		Emit()
}
