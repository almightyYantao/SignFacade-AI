package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	defaultApimartBaseURL        = "https://api.apimart.ai"
	defaultApimartModel          = "gemini-3.1-flash-image-preview"
	defaultApimartCreateTaskPath = "/v1/images/generations"
	defaultApimartTaskStatusPath = "/v1/tasks"
	defaultApimartSize           = "1:1"
	defaultApimartResolution     = "2K"
	defaultApimartLanguage       = "zh"
)

type apimartClient struct {
	baseURL           string
	apiKey            string
	model             string
	createTaskPath    string
	taskStatusPath    string
	usagePath         string
	size              string
	defaultResolution string
	imageCount        int
	language          string
	pollInterval      time.Duration
}

func newApimartClientFromEnv() (aiClient, error) {
	apiKey, err := getRequiredEnv("APIMART_API_KEY")
	if err != nil {
		return nil, err
	}

	pollInterval := 2 * time.Second
	if v := strings.TrimSpace(os.Getenv("APIMART_POLL_INTERVAL_SECONDS")); v != "" {
		if sec, convErr := strconv.Atoi(v); convErr == nil && sec > 0 {
			pollInterval = time.Duration(sec) * time.Second
		}
	}

	imageCount := 1
	if v := strings.TrimSpace(os.Getenv("APIMART_IMAGE_COUNT")); v != "" {
		if n, convErr := strconv.Atoi(v); convErr == nil && n > 0 {
			imageCount = n
		}
	}

	client := &apimartClient{
		baseURL:           strings.TrimRight(envOrDefault("APIMART_BASE_URL", defaultApimartBaseURL), "/"),
		apiKey:            apiKey,
		model:             envOrDefault("APIMART_MODEL", defaultApimartModel),
		createTaskPath:    envOrDefault("APIMART_CREATE_TASK_PATH", defaultApimartCreateTaskPath),
		taskStatusPath:    envOrDefault("APIMART_TASK_STATUS_PATH", defaultApimartTaskStatusPath),
		usagePath:         strings.TrimSpace(os.Getenv("APIMART_USAGE_PATH")),
		size:              envOrDefault("APIMART_SIZE", defaultApimartSize),
		defaultResolution: envOrDefault("APIMART_DEFAULT_RESOLUTION", defaultApimartResolution),
		imageCount:        imageCount,
		language:          envOrDefault("APIMART_STATUS_LANGUAGE", defaultApimartLanguage),
		pollInterval:      pollInterval,
	}
	log.Printf("[apimart-init] model=%s base_url=%s", client.model, client.baseURL)
	return client, nil
}

func (c *apimartClient) Provider() string {
	return "apimart"
}

func (c *apimartClient) CreateTask(input aiCreateTaskInput) (aiCreateTaskResult, error) {
	resolution := strings.TrimSpace(input.Resolution)
	if resolution == "" {
		resolution = c.defaultResolution
	}

	payload := map[string]interface{}{
		"model":  c.model,
		"prompt": input.Prompt,
		"size":   c.size,
		"n":      c.imageCount,
	}
	if resolution != "" {
		payload["resolution"] = resolution
	}
	if len(input.ImageList) > 0 {
		payload["image_urls"] = input.ImageList
	}

	log.Printf(
		"[apimart-create] model=%s prompt_len=%d image_count=%d payload_keys=%v",
		c.model,
		len(input.Prompt),
		len(input.ImageList),
		mapKeys(payload),
	)

	body, statusCode, err := c.doJSONRequest(http.MethodPost, c.createTaskPath, payload)
	if err != nil {
		return aiCreateTaskResult{}, err
	}

	result := aiCreateTaskResult{
		StatusCode: statusCode,
		Body:       body,
		Success:    false,
	}
	if statusCode < http.StatusOK || statusCode >= http.StatusMultipleChoices {
		log.Printf("[apimart-create] non-2xx status=%d body=%s", statusCode, truncateLog(strings.TrimSpace(string(body)), 320))
		return result, nil
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return aiCreateTaskResult{}, fmt.Errorf("invalid apimart createTask response: %w", err)
	}
	if code, ok := toInt(parsed["code"]); ok && code != http.StatusOK {
		log.Printf(
			"[apimart-create] rejected code=%v message=%s",
			parsed["code"],
			truncateLog(firstNonEmptyString(getString(parsed, "message"), getString(parsed, "msg")), 320),
		)
		return result, nil
	}

	taskID, state := parseApimartCreateData(parsed["data"])
	if taskID == "" {
		return result, nil
	}

	normalized := createTaskResponse{
		TaskID: taskID,
		Status: mapApimartState(state),
	}
	if normalized.Status == "" {
		normalized.Status = "PENDING"
	}

	normalizedBody, err := json.Marshal(normalized)
	if err != nil {
		return aiCreateTaskResult{}, err
	}

	result.TaskID = normalized.TaskID
	result.Status = normalized.Status
	result.StatusCode = http.StatusAccepted
	result.Body = normalizedBody
	result.Success = true
	return result, nil
}

func (c *apimartClient) WaitTask(taskID string, timeoutSeconds int) ([]byte, int, error) {
	if timeoutSeconds <= 0 {
		timeoutSeconds = 60
	}

	deadline := time.Now().Add(time.Duration(timeoutSeconds) * time.Second)
	var lastBody []byte
	lastStatus := http.StatusOK
	for {
		body, statusCode, err := c.GetTaskStatus(taskID)
		if err != nil {
			return nil, statusCode, err
		}
		lastBody = body
		lastStatus = statusCode

		if statusCode >= http.StatusBadRequest {
			return body, statusCode, nil
		}

		status := readTaskStatus(body)
		if status == "SUCCESS" || status == "FAILED" {
			return body, http.StatusOK, nil
		}

		if time.Now().After(deadline) {
			break
		}
		time.Sleep(c.pollInterval)
	}

	if len(lastBody) > 0 {
		return lastBody, lastStatus, nil
	}
	timeoutBody, _ := json.Marshal(apiError{Error: "wait timeout"})
	return timeoutBody, http.StatusGatewayTimeout, nil
}

func (c *apimartClient) GetTaskStatus(taskID string) ([]byte, int, error) {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		body, _ := json.Marshal(apiError{Error: "task_id is required"})
		return body, http.StatusBadRequest, nil
	}

	taskURL, err := url.Parse(strings.TrimRight(joinURL(c.baseURL, c.taskStatusPath), "/") + "/" + url.PathEscape(taskID))
	if err != nil {
		return nil, http.StatusInternalServerError, err
	}
	if strings.TrimSpace(c.language) != "" {
		query := taskURL.Query()
		query.Set("language", strings.TrimSpace(c.language))
		taskURL.RawQuery = query.Encode()
	}

	body, statusCode, err := c.doRawRequest(http.MethodGet, taskURL.String(), nil)
	if err != nil {
		return nil, http.StatusBadGateway, err
	}
	if statusCode < http.StatusOK || statusCode >= http.StatusMultipleChoices {
		return body, statusCode, nil
	}

	normalized, normErr := normalizeApimartStatusResponse(taskID, body)
	if normErr != nil {
		return body, statusCode, nil
	}
	return normalized, http.StatusOK, nil
}

func (c *apimartClient) GetUsage() ([]byte, int, error) {
	path := strings.TrimSpace(c.usagePath)
	if path == "" {
		body, _ := json.Marshal(map[string]interface{}{
			"provider": c.Provider(),
			"message":  "APIMART_USAGE_PATH not configured",
		})
		return body, http.StatusOK, nil
	}

	fullURL := path
	if !strings.HasPrefix(path, "http://") && !strings.HasPrefix(path, "https://") {
		fullURL = joinURL(c.baseURL, path)
	}

	body, statusCode, err := c.doRawRequest(http.MethodGet, fullURL, nil)
	if err != nil {
		return nil, http.StatusBadGateway, err
	}
	return body, statusCode, nil
}

func (c *apimartClient) doJSONRequest(method, path string, payload interface{}) ([]byte, int, error) {
	reqBody, err := json.Marshal(payload)
	if err != nil {
		return nil, http.StatusInternalServerError, err
	}
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return c.doRawRequest(method, path, reqBody)
	}
	return c.doRawRequest(method, joinURL(c.baseURL, path), reqBody)
}

func (c *apimartClient) doRawRequest(method, fullURL string, body []byte) ([]byte, int, error) {
	startedAt := time.Now()
	var reader io.Reader
	if len(body) > 0 {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, fullURL, reader)
	if err != nil {
		return nil, http.StatusInternalServerError, err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Accept", "application/json")
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}

	client := &http.Client{Timeout: 90 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("[apimart-http] %s %s failed in %s err=%v", method, sanitizeURLForLog(fullURL), time.Since(startedAt), err)
		return nil, http.StatusBadGateway, err
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("[apimart-http] %s %s read failed in %s err=%v", method, sanitizeURLForLog(fullURL), time.Since(startedAt), err)
		return nil, http.StatusBadGateway, err
	}
	elapsed := time.Since(startedAt)
	if resp.StatusCode >= http.StatusBadRequest {
		log.Printf(
			"[apimart-http] %s %s -> %d in %s body=%s",
			method,
			sanitizeURLForLog(fullURL),
			resp.StatusCode,
			elapsed,
			truncateLog(strings.TrimSpace(string(bodyBytes)), 320),
		)
	} else if elapsed > 3*time.Second {
		log.Printf("[apimart-http] %s %s -> %d in %s", method, sanitizeURLForLog(fullURL), resp.StatusCode, elapsed)
	}
	return bodyBytes, resp.StatusCode, nil
}

func parseApimartCreateData(data interface{}) (string, string) {
	switch v := data.(type) {
	case map[string]interface{}:
		taskID := firstNonEmptyString(getString(v, "task_id"), getString(v, "taskId"), getString(v, "id"))
		state := firstNonEmptyString(getString(v, "status"), getString(v, "state"))
		return strings.TrimSpace(taskID), strings.TrimSpace(state)
	case []interface{}:
		for _, item := range v {
			if m, ok := item.(map[string]interface{}); ok {
				taskID := firstNonEmptyString(getString(m, "task_id"), getString(m, "taskId"), getString(m, "id"))
				state := firstNonEmptyString(getString(m, "status"), getString(m, "state"))
				if strings.TrimSpace(taskID) != "" {
					return strings.TrimSpace(taskID), strings.TrimSpace(state)
				}
			}
		}
	}
	return "", ""
}

func normalizeApimartStatusResponse(taskID string, body []byte) ([]byte, error) {
	var wrapper map[string]interface{}
	if err := json.Unmarshal(body, &wrapper); err != nil {
		return nil, err
	}

	if code, ok := toInt(wrapper["code"]); ok && code != http.StatusOK {
		normalized := map[string]interface{}{
			"task_id": taskID,
			"status":  "FAILED",
		}
		if msg := firstNonEmptyString(getString(wrapper, "message"), getString(wrapper, "msg")); strings.TrimSpace(msg) != "" {
			normalized["error_msg"] = msg
		}
		return json.Marshal(normalized)
	}

	data, ok := wrapper["data"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("missing data field")
	}

	normalized := map[string]interface{}{
		"task_id": firstNonEmptyString(getString(data, "id"), getString(data, "task_id"), getString(data, "taskId"), taskID),
		"status":  "PENDING",
	}

	if state := firstNonEmptyString(getString(data, "status"), getString(data, "state")); state != "" {
		normalized["status"] = mapApimartState(state)
	}
	if progress, ok := normalizeProgress(data["progress"]); ok {
		normalized["progress"] = progress
	}

	urls := extractApimartResultURLs(data)
	if len(urls) > 0 {
		normalized["result"] = urls
	}

	errorMsg := firstNonEmptyString(
		getString(data, "error"),
		getString(data, "error_message"),
		getString(data, "message"),
		getString(data, "msg"),
	)
	if strings.TrimSpace(errorMsg) != "" {
		normalized["error_msg"] = errorMsg
		if normalized["status"] == "PENDING" {
			normalized["status"] = "FAILED"
		}
	}

	return json.Marshal(normalized)
}

func mapApimartState(state string) string {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "completed", "complete", "succeeded", "success", "done", "finished":
		return "SUCCESS"
	case "failed", "fail", "error", "cancelled", "canceled", "rejected", "timeout", "expired":
		return "FAILED"
	default:
		return "PENDING"
	}
}

func extractApimartResultURLs(data map[string]interface{}) []string {
	urls := make([]string, 0)
	urls = append(urls, extractURLsFromValue(data["result"])...)
	urls = append(urls, extractURLsFromValue(data["images"])...)

	if resultMap, ok := data["result"].(map[string]interface{}); ok {
		urls = append(urls, extractURLsFromValue(resultMap["images"])...)
		urls = append(urls, extractURLsFromValue(resultMap["url"])...)
		urls = append(urls, extractURLsFromValue(resultMap["urls"])...)
	}

	return dedupeStrings(urls)
}
