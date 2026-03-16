package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
)

const defaultLegacyNanoBaseURL = "http://35.243.92.91:9009/v1/nano-banana-pro"

type legacyNanoClient struct {
	baseURL string
	apiKey  string
}

func newLegacyNanoClientFromEnv() (aiClient, error) {
	apiKey, err := getRequiredEnv("NANO_BANANA_API_KEY")
	if err != nil {
		return nil, err
	}

	baseURL := strings.TrimRight(strings.TrimSpace(os.Getenv("NANO_BANANA_BASE_URL")), "/")
	if baseURL == "" {
		baseURL = defaultLegacyNanoBaseURL
	}

	return &legacyNanoClient{
		baseURL: baseURL,
		apiKey:  apiKey,
	}, nil
}

func (c *legacyNanoClient) Provider() string {
	return "legacy"
}

func (c *legacyNanoClient) CreateTask(input aiCreateTaskInput) (aiCreateTaskResult, error) {
	payload := map[string]interface{}{
		"api_key":    c.apiKey,
		"prompt":     input.Prompt,
		"image_list": input.ImageList,
		"resolution": input.Resolution,
	}
	if len(input.ImageList) == 0 {
		delete(payload, "image_list")
	}
	if strings.TrimSpace(input.Resolution) == "" {
		delete(payload, "resolution")
	}

	body, statusCode, err := proxyRequestRaw(http.MethodPost, c.baseURL+"/async", payload)
	if err != nil {
		return aiCreateTaskResult{}, err
	}

	result := aiCreateTaskResult{
		StatusCode: statusCode,
		Body:       body,
		Success:    false,
	}

	if statusCode != http.StatusAccepted {
		return result, nil
	}

	var created createTaskResponse
	if err := json.Unmarshal(body, &created); err != nil {
		return aiCreateTaskResult{}, errors.New("invalid create-task response")
	}
	if strings.TrimSpace(created.TaskID) == "" {
		return aiCreateTaskResult{}, errors.New("missing task_id in create-task response")
	}
	if strings.TrimSpace(created.Status) == "" {
		created.Status = "PENDING"
	}

	result.TaskID = created.TaskID
	result.Status = created.Status
	result.Success = true
	return result, nil
}

func (c *legacyNanoClient) WaitTask(taskID string, timeoutSeconds int) ([]byte, int, error) {
	payload := map[string]interface{}{
		"api_key": c.apiKey,
		"task_id": taskID,
	}
	if timeoutSeconds > 0 {
		payload["timeout_seconds"] = timeoutSeconds
	}
	return proxyRequestRaw(http.MethodPost, c.baseURL+"/wait", payload)
}

func (c *legacyNanoClient) GetTaskStatus(taskID string) ([]byte, int, error) {
	u := fmt.Sprintf(
		"%s/status/%s?api_key=%s",
		c.baseURL,
		url.PathEscape(taskID),
		url.QueryEscape(c.apiKey),
	)
	return proxyGetRaw(u)
}

func (c *legacyNanoClient) GetUsage() ([]byte, int, error) {
	u := fmt.Sprintf("%s/usage?api_key=%s", c.baseURL, url.QueryEscape(c.apiKey))
	return proxyGetRaw(u)
}
