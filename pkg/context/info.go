package context

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Info holds context information about where a query is being run
type Info struct {
	WorkingDir string
	GitRepo    string
	GitBranch  string
	GitCommit  string
}

// GetContextInfo gathers context information about the current execution environment
func GetContextInfo(workDir string) *Info {
	info := &Info{
		WorkingDir: workDir,
	}

	if workDir == "" {
		if cwd, err := os.Getwd(); err == nil {
			info.WorkingDir = cwd
		}
	}

	if info.WorkingDir != "" {
		if output, err := runGitCommand(info.WorkingDir, "remote", "get-url", "origin"); err == nil {
			info.GitRepo = cleanGitURL(strings.TrimSpace(output))
		}
		if output, err := runGitCommand(info.WorkingDir, "branch", "--show-current"); err == nil {
			info.GitBranch = strings.TrimSpace(output)
		}
		if output, err := runGitCommand(info.WorkingDir, "rev-parse", "--short", "HEAD"); err == nil {
			info.GitCommit = strings.TrimSpace(output)
		}
	}

	return info
}

func runGitCommand(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.Output()
	return string(output), err
}

func cleanGitURL(url string) string {
	url = strings.TrimSuffix(url, ".git")
	if strings.HasPrefix(url, "git@") {
		parts := strings.Split(url, ":")
		if len(parts) == 2 {
			host := strings.TrimPrefix(parts[0], "git@")
			return host + "/" + parts[1]
		}
	}
	if strings.HasPrefix(url, "https://") {
		url = strings.TrimPrefix(url, "https://")
		if atIndex := strings.Index(url, "@"); atIndex != -1 {
			url = url[atIndex+1:]
		}
	}
	return url
}

// GetCaller returns the name of the executable that is calling this function
func GetCaller() string {
	if exe, err := os.Executable(); err == nil {
		baseName := filepath.Base(exe)
		if baseName == "grove" || strings.HasPrefix(baseName, "grove-") {
			return baseName
		}
		return baseName
	}
	return "unknown"
}
