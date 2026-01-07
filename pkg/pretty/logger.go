package pretty

import (
	"context"
	"fmt"
	"io"
	"runtime"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	corelogging "github.com/mattsolo1/grove-core/logging"
	"github.com/mattsolo1/grove-core/tui/theme"
	"github.com/sirupsen/logrus"
)

// Logger is a wrapper around the grove-core PrettyLogger with Anthropic-specific helpers.
type Logger struct {
	*corelogging.PrettyLogger
	writer io.Writer
	theme  *theme.Theme
	log    *logrus.Entry
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
		writer:       corelogging.GetGlobalOutput(),
		theme:        theme.DefaultTheme,
		log:          corelogging.NewLogger("grove-anthropic"),
	}
}

// NewWithLogger creates a new logger with a specific structured logging backend.
func NewWithLogger(log *logrus.Entry) *Logger {
	return &Logger{
		PrettyLogger: corelogging.NewPrettyLogger(),
		writer:       corelogging.GetGlobalOutput(),
		theme:        theme.DefaultTheme,
		log:          log,
	}
}

// NewWithWriter creates a new Logger with a custom writer
func NewWithWriter(w io.Writer) *Logger {
	return &Logger{
		PrettyLogger: corelogging.NewPrettyLogger().WithWriter(w),
		writer:       w,
		theme:        theme.DefaultTheme,
		log:          corelogging.NewLogger("grove-anthropic"),
	}
}

// WorkingDirectoryCtx logs the working directory to the writer from the context
func (l *Logger) WorkingDirectoryCtx(ctx context.Context, dir string) {
	writer := corelogging.GetWriter(ctx)
	pathStyle := lipgloss.NewStyle().Italic(true)
	fmt.Fprintf(writer, "%s Working directory: %s\n",
		theme.IconHome,
		pathStyle.Render(dir))
	fmt.Fprintln(writer)
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
	fmt.Fprintf(l.writer, "%s %s\n",
		l.theme.Error.Render(theme.IconError),
		l.theme.Error.Render(message))
}

// ModelCtx logs the model being used to the writer from the context
func (l *Logger) ModelCtx(ctx context.Context, model string) {
	writer := corelogging.GetWriter(ctx)
	if l.log != nil {
		modelFields := ModelFields{Model: model}
		fields := corelogging.StructToLogrusFields(modelFields)
		if pc, file, line, ok := runtime.Caller(1); ok {
			fields["file"] = fmt.Sprintf("%s:%d", file, line)
			if fn := runtime.FuncForPC(pc); fn != nil {
				fields["func"] = fn.Name()
			}
		}
		entry := l.log.WithFields(fields)
		entry.Info("Calling Anthropic API")
	}
	fmt.Fprintf(writer, "%s Calling Anthropic API with model: %s\n",
		theme.IconRobot,
		model)
}

// Model logs the model being used
func (l *Logger) Model(model string) {
	l.ModelCtx(context.Background(), model)
}

// UploadProgressCtx logs file upload progress to the writer from the context
func (l *Logger) UploadProgressCtx(ctx context.Context, message string) {
	writer := corelogging.GetWriter(ctx)
	fmt.Fprintf(writer, "%s %s\n", theme.IconRunning, message)
}

// UploadProgress logs file upload progress
func (l *Logger) UploadProgress(message string) {
	l.UploadProgressCtx(context.Background(), message)
}

// UploadComplete logs successful file upload
func (l *Logger) UploadComplete(filename string, duration time.Duration) {
	fmt.Fprintf(l.writer, "%s %s %s\n",
		l.theme.Success.Render(theme.IconSuccess),
		l.theme.Success.Render(filename),
		l.theme.Muted.Render(fmt.Sprintf("(%.2fs)", duration.Seconds())))
}

// FilesIncludedCtx displays the list of files that will be included in the request
func (l *Logger) FilesIncludedCtx(ctx context.Context, files []string) {
	if len(files) == 0 {
		return
	}

	writer := corelogging.GetWriter(ctx)
	fmt.Fprintf(writer, "%s Files attached to request:\n", theme.IconFile)

	pathStyle := lipgloss.NewStyle().Italic(true)
	for _, file := range files {
		displayName := file
		if idx := strings.LastIndex(file, "/"); idx != -1 {
			displayName = file[idx+1:]
		}
		if displayName == "CLAUDE.md" || displayName == "context" || displayName == "cached-context" {
			fmt.Fprintf(writer, "%s %s\n", l.theme.Highlight.Render(theme.IconBullet), pathStyle.Render(file))
		} else {
			fmt.Fprintf(writer, "%s %s\n", l.theme.Highlight.Render(theme.IconBullet), pathStyle.Render(displayName))
		}
	}
}

// FilesIncluded displays the list of files that will be included in the request
func (l *Logger) FilesIncluded(files []string) {
	l.FilesIncludedCtx(context.Background(), files)
}

// TokenUsageCtx displays token usage statistics in a styled box
func (l *Logger) TokenUsageCtx(ctx context.Context, inputTokens, outputTokens int, responseTime time.Duration, estimatedCost float64) {
	writer := corelogging.GetWriter(ctx)

	totalTokens := inputTokens + outputTokens

	if l.log != nil {
		tokenFields := TokenFields{
			InputTokens:      inputTokens,
			OutputTokens:     outputTokens,
			TotalTokens:      totalTokens,
			ResponseTimeMs:   responseTime.Milliseconds(),
			EstimatedCostUSD: estimatedCost,
		}
		fields := corelogging.StructToLogrusFields(tokenFields)
		if pc, file, line, ok := runtime.Caller(1); ok {
			fields["file"] = fmt.Sprintf("%s:%d", file, line)
			if fn := runtime.FuncForPC(pc); fn != nil {
				fields["func"] = fn.Name()
			}
		}
		entry := l.log.WithFields(fields)
		entry.Info("Anthropic Response & Token Summary")
	}

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
	fmt.Fprintf(writer, "\n%s Token usage:\n%s\n", theme.IconChart, box)
}

// TokenUsage displays token usage statistics in a styled box
func (l *Logger) TokenUsage(inputTokens, outputTokens int, responseTime time.Duration, estimatedCost float64) {
	l.TokenUsageCtx(context.Background(), inputTokens, outputTokens, responseTime, estimatedCost)
}

// ResponseWritten logs successful response write
func (l *Logger) ResponseWritten(path string) {
	pathStyle := lipgloss.NewStyle().Foreground(theme.Cyan).Italic(true)
	fmt.Fprintf(l.writer, "%s %s %s\n",
		l.theme.Success.Render(theme.IconSuccess),
		l.theme.Success.Render("Response written to:"),
		pathStyle.Render(path))
}

// Tip logs a helpful tip
func (l *Logger) Tip(message string) {
	fmt.Fprintf(l.writer, "%s\n",
		l.theme.Info.Render(theme.IconLightbulb+" "+message))
}

// Progress logs a progress message
func (l *Logger) Progress(message string) {
	fmt.Fprintf(l.writer, "%s\n", message)
}

// Blank prints a blank line
func (l *Logger) Blank() {
	fmt.Fprintln(l.writer)
}

// Section prints a section header
func (l *Logger) Section(title string) {
	fmt.Fprintf(l.writer, "%s\n", l.theme.Header.Render(title))
}

// Field prints a labeled field
func (l *Logger) Field(label string, value interface{}) {
	fmt.Fprintf(l.writer, "%s %s %v\n",
		l.theme.Highlight.Render(theme.IconBullet),
		l.theme.Muted.Render(label+":"),
		value)
}
