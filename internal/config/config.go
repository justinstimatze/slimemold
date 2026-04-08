package config

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// Config holds runtime configuration.
type Config struct {
	AnthropicAPIKey string
	Model           string // extraction model, default claude-sonnet-4-6
	DataDir         string // ~/.slimemold
	KnowledgeMode   bool   // shifts extraction toward knowledge gaps
}

// Load reads config from environment, falling back to .env file.
func Load() (*Config, error) {
	// Try .env in cwd, then home
	loadDotenv(".env")
	if home, err := os.UserHomeDir(); err == nil {
		loadDotenv(home + "/.config/slimemold/.env")
	}

	apiKey := os.Getenv("ANTHROPIC_API_KEY")

	model := os.Getenv("SLIMEMOLD_MODEL")
	if model == "" {
		model = "claude-sonnet-4-6"
	}

	dataDir := os.Getenv("SLIMEMOLD_DATA_DIR")
	if dataDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("cannot determine home directory: %w", err)
		}
		dataDir = home + "/.slimemold"
	}

	mode := strings.ToLower(os.Getenv("SLIMEMOLD_MODE"))
	knowledgeMode := mode == "knowledge"

	return &Config{
		AnthropicAPIKey: apiKey,
		Model:           model,
		DataDir:         dataDir,
		KnowledgeMode:   knowledgeMode,
	}, nil
}

func loadDotenv(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		v = strings.Trim(v, `"'`)
		if os.Getenv(k) == "" {
			_ = os.Setenv(k, v)
		}
	}
}
