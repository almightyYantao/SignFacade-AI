package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// aiCreateTaskInput is the normalized input used by all AI providers.
type aiCreateTaskInput struct {
	Prompt     string
	ImageList  []string
	Resolution string
}

// aiCreateTaskResult is the normalized create-task result from providers.
type aiCreateTaskResult struct {
	TaskID     string
	Status     string
	StatusCode int
	Body       []byte
	Success    bool
}

type aiClient interface {
	Provider() string
	CreateTask(input aiCreateTaskInput) (aiCreateTaskResult, error)
	WaitTask(taskID string, timeoutSeconds int) ([]byte, int, error)
	GetTaskStatus(taskID string) ([]byte, int, error)
	GetUsage() ([]byte, int, error)
}

// aiImageUploader is optionally implemented by providers that can ingest
// image bytes directly and return a reusable public URL.
type aiImageUploader interface {
	UploadDataURL(filename, dataURL string) (string, error)
}

func newAIClientFromEnv() (aiClient, error) {
	// AI_PROVIDER controls provider routing:
	// - legacy / nano (default): old nano-banana async API
	// - kie: kie.ai Market API
	// - apimart: apimart.ai async image generation API
	provider := strings.ToLower(strings.TrimSpace(os.Getenv("AI_PROVIDER")))
	if provider == "" {
		provider = "legacy"
	}

	switch provider {
	case "legacy", "nano", "nano-legacy", "nanobanana":
		return newLegacyNanoClientFromEnv()
	case "kie", "kie.ai":
		return newKieClientFromEnv()
	case "apimart", "api-mart", "apimart.ai":
		return newApimartClientFromEnv()
	default:
		return nil, fmt.Errorf("unsupported AI_PROVIDER: %s", provider)
	}
}

func getRequiredEnv(key string) (string, error) {
	val := strings.TrimSpace(os.Getenv(key))
	if val == "" {
		return "", errors.New(key + " is not set")
	}
	return val, nil
}
