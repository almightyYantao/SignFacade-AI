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
	defaultKieBaseURL           = "https://api.kie.ai"
	defaultKieModel             = "nano-banana-2"
	defaultKieCreateTaskPath    = "/api/v1/jobs/createTask"
	defaultKieTaskStatusPath    = "/api/v1/jobs/recordInfo"
	defaultKieUsagePath         = "/api/v1/chat/credit"
	defaultKieFileUploadBaseURL = "https://kieai.redpandaai.co"
	defaultKieFileUploadPath    = "/api/file-base64-upload"
	defaultKieFileUploadDir     = "images/base64"
	defaultKieAspectRatio       = "1:1"
	defaultKieOutputFormat      = "png"
	defaultKieDefaultResolution = "2K"
)

type kieClient struct {
	baseURL            string
	apiKey             string
	model              string
	createTaskPath     string
	taskStatusPath     string
	usagePath          string
	fileUploadBaseURL  string
	fileUploadPath     string
	fileUploadDir      string
	aspectRatio        string
	enableGoogleSearch bool
	outputFormat       string
	defaultResolution  string
	pollInterval       time.Duration
}

func newKieClientFromEnv() (aiClient, error) {
	apiKey, err := getRequiredEnv("KIE_API_KEY")
	if err != nil {
		return nil, err
	}

	pollInterval := 2 * time.Second
	if v := strings.TrimSpace(os.Getenv("KIE_POLL_INTERVAL_SECONDS")); v != "" {
		if sec, convErr := strconv.Atoi(v); convErr == nil && sec > 0 {
			pollInterval = time.Duration(sec) * time.Second
		}
	}

	model := envOrDefault("KIE_MODEL", defaultKieModel)

	client := &kieClient{
		baseURL:            strings.TrimRight(envOrDefault("KIE_BASE_URL", defaultKieBaseURL), "/"),
		apiKey:             apiKey,
		model:              model,
		createTaskPath:     envOrDefault("KIE_CREATE_TASK_PATH", defaultKieCreateTaskPath),
		taskStatusPath:     envOrDefault("KIE_TASK_STATUS_PATH", defaultKieTaskStatusPath),
		usagePath:          envOrDefault("KIE_USAGE_PATH", defaultKieUsagePath),
		fileUploadBaseURL:  strings.TrimRight(envOrDefault("KIE_FILE_UPLOAD_BASE_URL", defaultKieFileUploadBaseURL), "/"),
		fileUploadPath:     envOrDefault("KIE_FILE_UPLOAD_PATH", defaultKieFileUploadPath),
		fileUploadDir:      strings.Trim(envOrDefault("KIE_FILE_UPLOAD_DIR", defaultKieFileUploadDir), "/"),
		aspectRatio:        envOrDefault("KIE_ASPECT_RATIO", defaultKieAspectRatio),
		enableGoogleSearch: envBool("KIE_GOOGLE_SEARCH"),
		outputFormat:       envOrDefault("KIE_OUTPUT_FORMAT", defaultKieOutputFormat),
		defaultResolution:  envOrDefault("KIE_DEFAULT_RESOLUTION", defaultKieDefaultResolution),
		pollInterval:       pollInterval,
	}
	log.Printf("[kie-init] model=%s base_url=%s", client.model, client.baseURL)
	return client, nil
}

func (c *kieClient) Provider() string {
	return "kie"
}

func (c *kieClient) CreateTask(input aiCreateTaskInput) (aiCreateTaskResult, error) {
	model := strings.TrimSpace(c.model)
	modelLower := strings.ToLower(model)
	isZImage := strings.HasPrefix(modelLower, "z-image")

	resolution := strings.TrimSpace(input.Resolution)
	if resolution == "" {
		resolution = c.defaultResolution
	}

	requestInput := map[string]interface{}{
		"prompt": input.Prompt,
	}
	if strings.TrimSpace(c.aspectRatio) != "" {
		requestInput["aspect_ratio"] = c.aspectRatio
	}
	if len(input.ImageList) > 0 {
		requestInput["image_input"] = input.ImageList
	}
	if !isZImage {
		if strings.TrimSpace(resolution) != "" {
			requestInput["resolution"] = resolution
		}
		if strings.TrimSpace(c.outputFormat) != "" {
			requestInput["output_format"] = c.outputFormat
		}
		if c.enableGoogleSearch {
			requestInput["google_search"] = true
		}
	}

	log.Printf(
		"[kie-create] model=%s is_z_image=%t prompt_len=%d image_count=%d input_keys=%v",
		model,
		isZImage,
		len(input.Prompt),
		len(input.ImageList),
		mapKeys(requestInput),
	)

	payload := map[string]interface{}{
		"model": model,
		"input": requestInput,
	}
	if callback := strings.TrimSpace(os.Getenv("KIE_CALLBACK_URL")); callback != "" {
		payload["callBackUrl"] = callback
	}

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
		log.Printf("[kie-create] non-2xx model=%s status=%d body=%s", model, statusCode, truncateLog(strings.TrimSpace(string(body)), 320))
		return result, nil
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return aiCreateTaskResult{}, fmt.Errorf("invalid kie createTask response: %w", err)
	}
	code, ok := toInt(parsed["code"])
	if !ok || code != http.StatusOK {
		log.Printf(
			"[kie-create] rejected model=%s code=%v msg=%s",
			model,
			parsed["code"],
			truncateLog(strings.TrimSpace(getString(parsed, "msg")), 320),
		)
		return result, nil
	}
	data, _ := parsed["data"].(map[string]interface{})
	taskID := strings.TrimSpace(getString(data, "taskId"))
	if taskID == "" {
		return result, nil
	}

	normalized := createTaskResponse{
		TaskID: taskID,
		Status: "PENDING",
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

func (c *kieClient) WaitTask(taskID string, timeoutSeconds int) ([]byte, int, error) {
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

func (c *kieClient) GetTaskStatus(taskID string) ([]byte, int, error) {
	statusURL, err := url.Parse(joinURL(c.baseURL, c.taskStatusPath))
	if err != nil {
		return nil, http.StatusInternalServerError, err
	}
	query := statusURL.Query()
	query.Set("taskId", taskID)
	statusURL.RawQuery = query.Encode()

	body, statusCode, err := c.doRawRequest(http.MethodGet, statusURL.String(), nil)
	if err != nil {
		return nil, http.StatusBadGateway, err
	}
	if statusCode < http.StatusOK || statusCode >= http.StatusMultipleChoices {
		return body, statusCode, nil
	}

	normalized, normErr := normalizeKieStatusResponse(taskID, body)
	if normErr != nil {
		return body, statusCode, nil
	}
	return normalized, http.StatusOK, nil
}

func (c *kieClient) GetUsage() ([]byte, int, error) {
	body, statusCode, err := c.doRawRequest(http.MethodGet, joinURL(c.baseURL, c.usagePath), nil)
	if err != nil {
		return nil, http.StatusBadGateway, err
	}
	return body, statusCode, nil
}

func (c *kieClient) UploadDataURL(filename, dataURL string) (string, error) {
	filename = strings.TrimSpace(filename)
	if strings.TrimSpace(filename) == "" {
		filename = fmt.Sprintf("upload_%d", time.Now().UTC().UnixNano())
	}
	startedAt := time.Now()
	log.Printf("[kie-upload] start filename=%s data_url_len=%d", filename, len(dataURL))

	uploadURL, err := url.Parse(joinURL(c.fileUploadBaseURL, c.fileUploadPath))
	if err != nil {
		log.Printf("[kie-upload] invalid upload url filename=%s err=%v", filename, err)
		return "", err
	}

	payload := map[string]interface{}{
		"base64Data": dataURL,
		"uploadPath": c.fileUploadDir,
		"fileName":   filename,
	}
	body, statusCode, err := c.doJSONRequest(http.MethodPost, uploadURL.String(), payload)
	if err != nil {
		log.Printf("[kie-upload] request failed filename=%s elapsed=%s err=%v", filename, time.Since(startedAt), err)
		return "", err
	}
	if statusCode < http.StatusOK || statusCode >= http.StatusMultipleChoices {
		log.Printf(
			"[kie-upload] non-2xx filename=%s status=%d elapsed=%s body=%s",
			filename,
			statusCode,
			time.Since(startedAt),
			truncateLog(strings.TrimSpace(string(body)), 320),
		)
		return "", fmt.Errorf("kie file upload failed: status=%d body=%s", statusCode, strings.TrimSpace(string(body)))
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(body, &parsed); err != nil {
		log.Printf("[kie-upload] invalid json filename=%s elapsed=%s err=%v", filename, time.Since(startedAt), err)
		return "", fmt.Errorf("invalid kie file upload response: %w", err)
	}
	if code, ok := toInt(parsed["code"]); !ok || code != http.StatusOK {
		msg := strings.TrimSpace(getString(parsed, "msg"))
		if msg == "" {
			msg = strings.TrimSpace(string(body))
		}
		log.Printf("[kie-upload] rejected filename=%s code=%v elapsed=%s msg=%s", filename, parsed["code"], time.Since(startedAt), truncateLog(msg, 320))
		return "", fmt.Errorf("kie file upload rejected: %s", msg)
	}

	data, _ := parsed["data"].(map[string]interface{})
	downloadURL := strings.TrimSpace(firstNonEmptyString(
		getString(data, "downloadUrl"),
		getString(data, "download_url"),
		getString(data, "url"),
	))
	if downloadURL == "" {
		log.Printf("[kie-upload] missing download url filename=%s elapsed=%s", filename, time.Since(startedAt))
		return "", fmt.Errorf("kie file upload response missing download url")
	}
	log.Printf("[kie-upload] success filename=%s elapsed=%s", filename, time.Since(startedAt))
	return downloadURL, nil
}

func (c *kieClient) doJSONRequest(method, path string, payload interface{}) ([]byte, int, error) {
	reqBody, err := json.Marshal(payload)
	if err != nil {
		return nil, http.StatusInternalServerError, err
	}
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return c.doRawRequest(method, path, reqBody)
	}
	return c.doRawRequest(method, joinURL(c.baseURL, path), reqBody)
}

func (c *kieClient) doRawRequest(method, fullURL string, body []byte) ([]byte, int, error) {
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
		log.Printf("[kie-http] %s %s failed in %s err=%v", method, sanitizeURLForLog(fullURL), time.Since(startedAt), err)
		return nil, http.StatusBadGateway, err
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("[kie-http] %s %s read failed in %s err=%v", method, sanitizeURLForLog(fullURL), time.Since(startedAt), err)
		return nil, http.StatusBadGateway, err
	}
	elapsed := time.Since(startedAt)
	if resp.StatusCode >= http.StatusBadRequest {
		log.Printf(
			"[kie-http] %s %s -> %d in %s body=%s",
			method,
			sanitizeURLForLog(fullURL),
			resp.StatusCode,
			elapsed,
			truncateLog(strings.TrimSpace(string(bodyBytes)), 320),
		)
	} else if elapsed > 3*time.Second {
		log.Printf("[kie-http] %s %s -> %d in %s", method, sanitizeURLForLog(fullURL), resp.StatusCode, elapsed)
	}
	return bodyBytes, resp.StatusCode, nil
}

func normalizeKieStatusResponse(taskID string, body []byte) ([]byte, error) {
	var wrapper map[string]interface{}
	if err := json.Unmarshal(body, &wrapper); err != nil {
		return nil, err
	}

	data, ok := wrapper["data"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("missing data field")
	}

	normalized := map[string]interface{}{
		"task_id": firstNonEmptyString(getString(data, "taskId"), taskID),
		"status":  "PENDING",
	}

	if state := strings.ToLower(getString(data, "state")); state != "" {
		normalized["status"] = mapKieStateToStatus(state)
	}
	if flag, ok := toInt(data["successFlag"]); ok {
		switch flag {
		case 1:
			normalized["status"] = "SUCCESS"
		case 2:
			normalized["status"] = "FAILED"
		default:
			normalized["status"] = "PENDING"
		}
	}

	if progress, ok := normalizeProgress(data["progress"]); ok {
		normalized["progress"] = progress
	}

	errorMsg := firstNonEmptyString(getString(data, "failMsg"), getString(data, "errorMessage"))
	if errorMsg != "" {
		normalized["error_msg"] = errorMsg
		if normalized["status"] == "PENDING" {
			normalized["status"] = "FAILED"
		}
	}

	urls := extractKieResultURLs(data)
	if len(urls) > 0 {
		normalized["result"] = urls
	}

	return json.Marshal(normalized)
}

func mapKieStateToStatus(state string) string {
	switch state {
	case "success", "succeeded", "done", "completed":
		return "SUCCESS"
	case "fail", "failed", "error", "canceled", "cancelled":
		return "FAILED"
	default:
		return "PENDING"
	}
}

func extractKieResultURLs(data map[string]interface{}) []string {
	urls := make([]string, 0)
	urls = append(urls, extractURLsFromValue(data["resultUrls"])...)
	urls = append(urls, extractURLsFromValue(data["result_urls"])...)
	urls = append(urls, extractURLsFromValue(data["result"])...)

	if responseMap, ok := data["response"].(map[string]interface{}); ok {
		urls = append(urls, extractURLsFromValue(responseMap["resultUrls"])...)
		urls = append(urls, extractURLsFromValue(responseMap["result_urls"])...)
		urls = append(urls, extractURLsFromValue(responseMap["images"])...)
		urls = append(urls, extractURLsFromValue(responseMap["output_images"])...)
	}

	if resultJSON := strings.TrimSpace(getString(data, "resultJson")); resultJSON != "" {
		var parsed interface{}
		if err := json.Unmarshal([]byte(resultJSON), &parsed); err == nil {
			urls = append(urls, extractURLsFromValue(parsed)...)
		}
	}

	return dedupeStrings(urls)
}

func extractURLsFromValue(value interface{}) []string {
	switch v := value.(type) {
	case string:
		if strings.HasPrefix(v, "http://") || strings.HasPrefix(v, "https://") {
			return []string{v}
		}
		return nil
	case []string:
		return dedupeStrings(v)
	case []interface{}:
		urls := make([]string, 0, len(v))
		for _, item := range v {
			urls = append(urls, extractURLsFromValue(item)...)
		}
		return dedupeStrings(urls)
	case map[string]interface{}:
		urls := make([]string, 0)
		for _, key := range []string{"url", "image_url", "resultUrls", "result_urls", "images", "output_images"} {
			urls = append(urls, extractURLsFromValue(v[key])...)
		}
		return dedupeStrings(urls)
	default:
		return nil
	}
}

func dedupeStrings(items []string) []string {
	if len(items) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(items))
	out := make([]string, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	return out
}

func normalizeProgress(value interface{}) (int, bool) {
	switch v := value.(type) {
	case float64:
		return progressFromFloat(v)
	case float32:
		return progressFromFloat(float64(v))
	case int:
		return progressFromFloat(float64(v))
	case int64:
		return progressFromFloat(float64(v))
	case string:
		n, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil {
			return 0, false
		}
		return progressFromFloat(n)
	default:
		return 0, false
	}
}

func progressFromFloat(v float64) (int, bool) {
	if v < 0 {
		return 0, false
	}
	if v <= 1 {
		v = v * 100
	}
	if v > 100 {
		v = 100
	}
	return int(v + 0.5), true
}

func readTaskStatus(body []byte) string {
	var payload struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return ""
	}
	return strings.TrimSpace(strings.ToUpper(payload.Status))
}

func getString(m map[string]interface{}, key string) string {
	if m == nil {
		return ""
	}
	val, ok := m[key]
	if !ok || val == nil {
		return ""
	}
	switch v := val.(type) {
	case string:
		return v
	default:
		return fmt.Sprintf("%v", v)
	}
}

func toInt(v interface{}) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		return int(n), true
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(n))
		if err != nil {
			return 0, false
		}
		return parsed, true
	default:
		return 0, false
	}
}

func envOrDefault(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func envBool(key string) bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	return value == "1" || value == "true" || value == "yes" || value == "on"
}

func firstNonEmptyString(items ...string) string {
	for _, item := range items {
		if strings.TrimSpace(item) != "" {
			return item
		}
	}
	return ""
}

func joinURL(base, path string) string {
	return strings.TrimRight(base, "/") + "/" + strings.TrimLeft(path, "/")
}

func mapKeys(m map[string]interface{}) []string {
	if len(m) == 0 {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func sanitizeURLForLog(raw string) string {
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(raw)), "data:") {
		return fmt.Sprintf("data-url(len=%d)", len(raw))
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	parsed.RawQuery = ""
	return parsed.String()
}

func truncateLog(text string, maxLen int) string {
	if maxLen <= 0 {
		return ""
	}
	if len(text) <= maxLen {
		return text
	}
	return text[:maxLen] + "...(truncated)"
}
