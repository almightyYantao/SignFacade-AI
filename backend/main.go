package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"golang.org/x/crypto/bcrypt"
	_ "modernc.org/sqlite"
)

const (
	defaultCredits    = 20
	costPerImage      = 3
	sessionCookieName = "session_id"
)

type createTaskRequest struct {
	Slogan     string   `json:"slogan,omitempty"`
	Material   string   `json:"material,omitempty"`
	LightColor string   `json:"lightColor,omitempty"`
	Board      string   `json:"board,omitempty"`
	Glow       string   `json:"glow,omitempty"`
	Details    string   `json:"details,omitempty"`
	ImageList  []string `json:"image_list,omitempty"`
	Resolution string   `json:"resolution,omitempty"`
}

type waitTaskRequest struct {
	TaskID         string `json:"task_id"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
}

type apiError struct {
	Error string `json:"error"`
}

type loginRequest struct {
	Phone    string `json:"phone"`
	Password string `json:"password"`
	Captcha  string `json:"captcha,omitempty"`
}

type registerRequest struct {
	Phone    string `json:"phone"`
	Password string `json:"password"`
	Captcha  string `json:"captcha,omitempty"`
}

type rechargeRequest struct {
	Amount int `json:"amount"`
}

type presignRequest struct {
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
}

type presignResponse struct {
	UploadURL string `json:"upload_url"`
	FileURL   string `json:"file_url"`
	Key       string `json:"key"`
}

type createTaskResponse struct {
	TaskID string `json:"task_id"`
	Status string `json:"status"`
}

type userSummary struct {
	ID      int    `json:"id"`
	Phone   string `json:"phone"`
	Credits int    `json:"credits"`
}

type historyItem struct {
	ID        int       `json:"id"`
	TaskID    string    `json:"task_id"`
	Status    string    `json:"status"`
	Prompt    string    `json:"prompt"`
	Images    []string  `json:"images"`
	CreatedAt time.Time `json:"created_at"`
}

func main() {
	loadDotEnv(".env")

	db, err := initDB("data.db")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	captchaStore := newCaptchaStore()
	aiClient, err := newAIClientFromEnv()
	if err != nil {
		log.Fatal(err)
	}

	mux := http.NewServeMux()

	mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	mux.HandleFunc("/api/config", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"captcha_enabled": captchaEnabled(),
			"ai_provider":     aiClient.Provider(),
		})
	})

	mux.HandleFunc("/api/auth/captcha", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, apiError{Error: "method not allowed"})
			return
		}
		var payload struct {
			Phone string `json:"phone"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			writeJSON(w, http.StatusBadRequest, apiError{Error: "invalid json"})
			return
		}
		if strings.TrimSpace(payload.Phone) == "" {
			writeJSON(w, http.StatusBadRequest, apiError{Error: "phone is required"})
			return
		}
		code := captchaStore.Generate(payload.Phone, 5*time.Minute)
		resp := map[string]interface{}{"sent": true, "captcha_enabled": captchaEnabled()}
		if !captchaEnabled() {
			resp["dev_code"] = code
		}
		writeJSON(w, http.StatusOK, resp)
	})

	mux.HandleFunc("/api/auth/register", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, apiError{Error: "method not allowed"})
			return
		}
		var req registerRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, apiError{Error: "invalid json"})
			return
		}
		if strings.TrimSpace(req.Phone) == "" || strings.TrimSpace(req.Password) == "" {
			writeJSON(w, http.StatusBadRequest, apiError{Error: "phone and password are required"})
			return
		}
		if captchaEnabled() {
			if !captchaStore.Verify(req.Phone, req.Captcha) {
				writeJSON(w, http.StatusBadRequest, apiError{Error: "invalid captcha"})
				return
			}
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, apiError{Error: "failed to hash password"})
			return
		}
		res, err := db.Exec(`INSERT INTO users (phone, password_hash, credits, created_at) VALUES (?, ?, ?, ?)`, req.Phone, string(hash), defaultCredits, time.Now().UTC())
		if err != nil {
			if strings.Contains(err.Error(), "UNIQUE") {
				writeJSON(w, http.StatusConflict, apiError{Error: "phone already exists"})
				return
			}
			writeJSON(w, http.StatusInternalServerError, apiError{Error: "failed to create user"})
			return
		}
		userID, _ := res.LastInsertId()
		sessionID, err := createSession(db, int(userID))
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, apiError{Error: "failed to create session"})
			return
		}
		setSessionCookie(w, sessionID)
		user, _ := getUserByID(db, int(userID))
		writeJSON(w, http.StatusOK, user)
	})

	mux.HandleFunc("/api/auth/login", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, apiError{Error: "method not allowed"})
			return
		}
		var req loginRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, apiError{Error: "invalid json"})
			return
		}
		if strings.TrimSpace(req.Phone) == "" || strings.TrimSpace(req.Password) == "" {
			writeJSON(w, http.StatusBadRequest, apiError{Error: "phone and password are required"})
			return
		}
		if captchaEnabled() {
			if !captchaStore.Verify(req.Phone, req.Captcha) {
				writeJSON(w, http.StatusBadRequest, apiError{Error: "invalid captcha"})
				return
			}
		}
		user, hash, err := getUserByPhone(db, req.Phone)
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, apiError{Error: "invalid credentials"})
			return
		}
		if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(req.Password)); err != nil {
			writeJSON(w, http.StatusUnauthorized, apiError{Error: "invalid credentials"})
			return
		}
		sessionID, err := createSession(db, user.ID)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, apiError{Error: "failed to create session"})
			return
		}
		setSessionCookie(w, sessionID)
		writeJSON(w, http.StatusOK, user)
	})

	mux.HandleFunc("/api/auth/me", func(w http.ResponseWriter, r *http.Request) {
		user, err := requireUser(db, r)
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, apiError{Error: "unauthorized"})
			return
		}
		writeJSON(w, http.StatusOK, user)
	})

	mux.HandleFunc("/api/auth/logout", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, apiError{Error: "method not allowed"})
			return
		}
		sessionID := getSessionID(r)
		if sessionID != "" {
			_, _ = db.Exec("DELETE FROM sessions WHERE id = ?", sessionID)
		}
		http.SetCookie(w, &http.Cookie{
			Name:     sessionCookieName,
			Value:    "",
			Path:     "/",
			Expires:  time.Unix(0, 0),
			HttpOnly: true,
		})
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	mux.HandleFunc("/api/credits/recharge", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, apiError{Error: "method not allowed"})
			return
		}
		user, err := requireUser(db, r)
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, apiError{Error: "unauthorized"})
			return
		}
		var req rechargeRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, apiError{Error: "invalid json"})
			return
		}
		if req.Amount <= 0 {
			writeJSON(w, http.StatusBadRequest, apiError{Error: "amount must be positive"})
			return
		}
		if err := addCredits(db, user.ID, req.Amount, "充值"); err != nil {
			writeJSON(w, http.StatusInternalServerError, apiError{Error: "failed to recharge"})
			return
		}
		updated, _ := getUserByID(db, user.ID)
		writeJSON(w, http.StatusOK, updated)
	})

	mux.HandleFunc("/api/uploads/presign", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, apiError{Error: "method not allowed"})
			return
		}
		_, err := requireUser(db, r)
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, apiError{Error: "unauthorized"})
			return
		}
		var req presignRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, apiError{Error: "invalid json"})
			return
		}
		if strings.TrimSpace(req.Filename) == "" || strings.TrimSpace(req.ContentType) == "" {
			writeJSON(w, http.StatusBadRequest, apiError{Error: "filename and content_type are required"})
			return
		}
		resp, err := presignUpload(req.Filename, req.ContentType)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, apiError{Error: err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, resp)
	})

	mux.HandleFunc("/api/uploads/kie", func(w http.ResponseWriter, r *http.Request) {
		startedAt := time.Now()
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, apiError{Error: "method not allowed"})
			return
		}
		user, err := requireUser(db, r)
		if err != nil {
			log.Printf("[upload-kie] unauthorized: err=%v", err)
			writeJSON(w, http.StatusUnauthorized, apiError{Error: "unauthorized"})
			return
		}

		uploader, ok := aiClient.(aiImageUploader)
		if !ok {
			log.Printf("[upload-kie] unsupported provider=%s", aiClient.Provider())
			writeJSON(w, http.StatusBadRequest, apiError{Error: "current ai provider does not support direct image upload"})
			return
		}

		var req struct {
			Filename    string `json:"filename"`
			ContentType string `json:"content_type"`
			DataURL     string `json:"data_url"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			log.Printf("[upload-kie] invalid json user=%d err=%v", user.ID, err)
			writeJSON(w, http.StatusBadRequest, apiError{Error: "invalid json"})
			return
		}
		if strings.TrimSpace(req.Filename) == "" {
			log.Printf("[upload-kie] missing filename user=%d", user.ID)
			writeJSON(w, http.StatusBadRequest, apiError{Error: "filename is required"})
			return
		}
		safeFilename := sanitizeFilename(req.Filename)
		if !strings.HasPrefix(strings.TrimSpace(req.DataURL), "data:") {
			log.Printf("[upload-kie] invalid data_url user=%d filename=%s", user.ID, safeFilename)
			writeJSON(w, http.StatusBadRequest, apiError{Error: "data_url must be a data URL"})
			return
		}
		log.Printf(
			"[upload-kie] start user=%d filename=%s content_type=%s data_url_len=%d",
			user.ID,
			safeFilename,
			strings.TrimSpace(req.ContentType),
			len(req.DataURL),
		)

		fileURL, err := uploader.UploadDataURL(safeFilename, req.DataURL)
		if err != nil {
			log.Printf(
				"[upload-kie] failed user=%d filename=%s elapsed=%s err=%v",
				user.ID,
				safeFilename,
				time.Since(startedAt),
				err,
			)
			writeJSON(w, http.StatusBadGateway, apiError{Error: err.Error()})
			return
		}
		log.Printf(
			"[upload-kie] success user=%d filename=%s elapsed=%s",
			user.ID,
			safeFilename,
			time.Since(startedAt),
		)
		writeJSON(w, http.StatusOK, map[string]string{
			"file_url": fileURL,
			"key":      safeFilename,
		})
	})

	mux.HandleFunc("/api/history", func(w http.ResponseWriter, r *http.Request) {
		user, err := requireUser(db, r)
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, apiError{Error: "unauthorized"})
			return
		}
		rows, err := db.Query(`SELECT id, task_id, status, prompt, images, created_at FROM tasks WHERE user_id = ? ORDER BY id DESC`, user.ID)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, apiError{Error: "failed to load history"})
			return
		}
		defer rows.Close()

		var list []historyItem
		for rows.Next() {
			var item historyItem
			var imagesJSON string
			if err := rows.Scan(&item.ID, &item.TaskID, &item.Status, &item.Prompt, &imagesJSON, &item.CreatedAt); err != nil {
				continue
			}
			_ = json.Unmarshal([]byte(imagesJSON), &item.Images)
			list = append(list, item)
		}
		if list == nil {
			list = []historyItem{}
		}
		writeJSON(w, http.StatusOK, list)
	})

	mux.HandleFunc("/api/tasks", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, apiError{Error: "method not allowed"})
			return
		}
		user, err := requireUser(db, r)
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, apiError{Error: "unauthorized"})
			return
		}

		var req createTaskRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, apiError{Error: "invalid json"})
			return
		}
		req.ImageList = normalizeImageList(req.ImageList)
		maxImageCount := 3
		allowDataImage := aiClient.Provider() == "apimart"
		if allowDataImage {
			maxImageCount = 14
		}
		if len(req.ImageList) > maxImageCount {
			writeJSON(w, http.StatusBadRequest, apiError{Error: fmt.Sprintf("image_list cannot exceed %d images", maxImageCount)})
			return
		}
		for idx, imageURL := range req.ImageList {
			if err := probeInputImageReference(r.Context(), imageURL, allowDataImage); err != nil {
				log.Printf("[task-create] reject image index=%d url=%s err=%v", idx+1, sanitizeURLForLog(imageURL), err)
				writeJSON(w, http.StatusBadRequest, apiError{
					Error: fmt.Sprintf("第 %d 张图片无法处理：%v", idx+1, err),
				})
				return
			}
		}

		if user.Credits < costPerImage {
			writeJSON(w, http.StatusPaymentRequired, apiError{Error: "insufficient credits"})
			return
		}

		prompt := buildPrompt(req)
		createResult, err := aiClient.CreateTask(aiCreateTaskInput{
			Prompt:     prompt,
			ImageList:  req.ImageList,
			Resolution: req.Resolution,
		})
		if err != nil {
			writeJSON(w, http.StatusBadGateway, apiError{Error: err.Error()})
			return
		}

		if !createResult.Success {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(createResult.StatusCode)
			_, _ = w.Write(createResult.Body)
			return
		}

		if err := deductCredits(db, user.ID, costPerImage, "生成广告图"); err != nil {
			writeJSON(w, http.StatusInternalServerError, apiError{Error: "failed to deduct credits"})
			return
		}

		_, _ = db.Exec(`INSERT INTO tasks (user_id, task_id, status, prompt, images, created_at) VALUES (?, ?, ?, ?, ?, ?)`, user.ID, createResult.TaskID, createResult.Status, prompt, "[]", time.Now().UTC())

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(createResult.StatusCode)
		_, _ = w.Write(createResult.Body)
	})

	mux.HandleFunc("/api/tasks/wait", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, apiError{Error: "method not allowed"})
			return
		}
		user, err := requireUser(db, r)
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, apiError{Error: "unauthorized"})
			return
		}

		var req waitTaskRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, apiError{Error: "invalid json"})
			return
		}
		if strings.TrimSpace(req.TaskID) == "" {
			writeJSON(w, http.StatusBadRequest, apiError{Error: "task_id is required"})
			return
		}

		respBody, statusCode, err := aiClient.WaitTask(req.TaskID, req.TimeoutSeconds)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, apiError{Error: err.Error()})
			return
		}
		if statusCode == http.StatusOK {
			updateTaskFromStatus(db, user.ID, req.TaskID, respBody)
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(statusCode)
		_, _ = w.Write(respBody)
	})

	mux.HandleFunc("/api/tasks/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, apiError{Error: "method not allowed"})
			return
		}
		user, err := requireUser(db, r)
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, apiError{Error: "unauthorized"})
			return
		}

		taskID := strings.TrimPrefix(r.URL.Path, "/api/tasks/")
		if taskID == "" {
			writeJSON(w, http.StatusBadRequest, apiError{Error: "task_id is required"})
			return
		}

		respBody, statusCode, err := aiClient.GetTaskStatus(taskID)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, apiError{Error: err.Error()})
			return
		}

		if statusCode == http.StatusOK {
			updateTaskFromStatus(db, user.ID, taskID, respBody)
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(statusCode)
		_, _ = w.Write(respBody)
	})

	mux.HandleFunc("/api/usage", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, apiError{Error: "method not allowed"})
			return
		}
		user, err := requireUser(db, r)
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, apiError{Error: "unauthorized"})
			return
		}
		_ = user

		respBody, statusCode, err := aiClient.GetUsage()
		if err != nil {
			writeJSON(w, http.StatusBadGateway, apiError{Error: err.Error()})
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(statusCode)
		_, _ = w.Write(respBody)
	})

	port := strings.TrimSpace(os.Getenv("PORT"))
	if port == "" {
		port = "8080"
	}
	addr := ":" + port

	server := &http.Server{
		Addr:         addr,
		Handler:      withCORS(mux),
		ReadTimeout:  20 * time.Second,
		WriteTimeout: 60 * time.Second,
	}

	log.Printf("backend listening on %s (ai_provider=%s)", addr, aiClient.Provider())
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

func loadDotEnv(path string) {
	file, err := os.Open(path)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("failed to open %s: %v", path, err)
		}
		return
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "export ") {
			line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		}
		idx := strings.Index(line, "=")
		if idx <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		if key == "" {
			continue
		}
		val := strings.TrimSpace(line[idx+1:])
		if len(val) >= 2 {
			if (val[0] == '"' && val[len(val)-1] == '"') || (val[0] == '\'' && val[len(val)-1] == '\'') {
				val = val[1 : len(val)-1]
			}
		}
		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		_ = os.Setenv(key, val)
	}
	if err := scanner.Err(); err != nil {
		log.Printf("failed to parse %s: %v", path, err)
	}
}

func initDB(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}

	schema := `
  CREATE TABLE IF NOT EXISTS users (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    phone TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    credits INTEGER NOT NULL DEFAULT 20,
    created_at DATETIME NOT NULL
  );
  CREATE TABLE IF NOT EXISTS sessions (
    id TEXT PRIMARY KEY,
    user_id INTEGER NOT NULL,
    created_at DATETIME NOT NULL,
    FOREIGN KEY(user_id) REFERENCES users(id)
  );
  CREATE TABLE IF NOT EXISTS tasks (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER NOT NULL,
    task_id TEXT NOT NULL,
    status TEXT NOT NULL,
    prompt TEXT NOT NULL,
    images TEXT NOT NULL,
    created_at DATETIME NOT NULL,
    FOREIGN KEY(user_id) REFERENCES users(id)
  );
  CREATE TABLE IF NOT EXISTS credit_ledger (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER NOT NULL,
    delta INTEGER NOT NULL,
    reason TEXT NOT NULL,
    created_at DATETIME NOT NULL,
    FOREIGN KEY(user_id) REFERENCES users(id)
  );
  `

	if _, err := db.Exec(schema); err != nil {
		return nil, err
	}
	return db, nil
}

func createSession(db *sql.DB, userID int) (string, error) {
	sessionID, err := randomToken(32)
	if err != nil {
		return "", err
	}
	_, err = db.Exec(`INSERT INTO sessions (id, user_id, created_at) VALUES (?, ?, ?)`, sessionID, userID, time.Now().UTC())
	if err != nil {
		return "", err
	}
	return sessionID, nil
}

func setSessionCookie(w http.ResponseWriter, sessionID string) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    sessionID,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

func getSessionID(r *http.Request) string {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		return ""
	}
	return cookie.Value
}

func requireUser(db *sql.DB, r *http.Request) (userSummary, error) {
	sessionID := getSessionID(r)
	if sessionID == "" {
		return userSummary{}, errors.New("missing session")
	}
	var userID int
	err := db.QueryRow(`SELECT user_id FROM sessions WHERE id = ?`, sessionID).Scan(&userID)
	if err != nil {
		return userSummary{}, err
	}
	return getUserByID(db, userID)
}

func getUserByID(db *sql.DB, userID int) (userSummary, error) {
	var user userSummary
	err := db.QueryRow(`SELECT id, phone, credits FROM users WHERE id = ?`, userID).Scan(&user.ID, &user.Phone, &user.Credits)
	return user, err
}

func getUserByPhone(db *sql.DB, phone string) (userSummary, string, error) {
	var user userSummary
	var hash string
	err := db.QueryRow(`SELECT id, phone, credits, password_hash FROM users WHERE phone = ?`, phone).Scan(&user.ID, &user.Phone, &user.Credits, &hash)
	if err != nil {
		return userSummary{}, "", err
	}
	return user, hash, nil
}

func addCredits(db *sql.DB, userID int, amount int, reason string) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	_, err = tx.Exec(`UPDATE users SET credits = credits + ? WHERE id = ?`, amount, userID)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	_, err = tx.Exec(`INSERT INTO credit_ledger (user_id, delta, reason, created_at) VALUES (?, ?, ?, ?)`, userID, amount, reason, time.Now().UTC())
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func deductCredits(db *sql.DB, userID int, amount int, reason string) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	_, err = tx.Exec(`UPDATE users SET credits = credits - ? WHERE id = ?`, amount, userID)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	_, err = tx.Exec(`INSERT INTO credit_ledger (user_id, delta, reason, created_at) VALUES (?, ?, ?, ?)`, userID, -amount, reason, time.Now().UTC())
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func updateTaskFromStatus(db *sql.DB, userID int, taskID string, respBody []byte) {
	var payload struct {
		Status       string        `json:"status"`
		Result       []string      `json:"result"`
		OutputImages []interface{} `json:"output_images"`
	}
	if err := json.Unmarshal(respBody, &payload); err != nil {
		return
	}
	images := payload.Result
	if len(images) == 0 && len(payload.OutputImages) > 0 {
		for _, entry := range payload.OutputImages {
			if m, ok := entry.(map[string]interface{}); ok {
				if url, ok := m["url"].(string); ok {
					images = append(images, url)
				}
			}
		}
	}
	imagesJSON, _ := json.Marshal(images)
	_, _ = db.Exec(`UPDATE tasks SET status = ?, images = ? WHERE user_id = ? AND task_id = ?`, payload.Status, string(imagesJSON), userID, taskID)
}

func randomToken(length int) (string, error) {
	buf := make([]byte, length)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func proxyRequest(w http.ResponseWriter, method, url string, payload interface{}) {
	body, err := json.Marshal(payload)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiError{Error: "failed to encode request"})
		return
	}

	req, err := http.NewRequest(method, url, bytes.NewBuffer(body))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiError{Error: "failed to build request"})
		return
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 90 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, apiError{Error: err.Error()})
		return
	}
	defer resp.Body.Close()

	copyResponse(w, resp)
}

func proxyRequestRaw(method, url string, payload interface{}) ([]byte, int, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, http.StatusInternalServerError, err
	}

	req, err := http.NewRequest(method, url, bytes.NewBuffer(body))
	if err != nil {
		return nil, http.StatusInternalServerError, err
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 90 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, http.StatusBadGateway, err
	}
	defer resp.Body.Close()
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, http.StatusBadGateway, err
	}
	return bodyBytes, resp.StatusCode, nil
}

func proxyGet(w http.ResponseWriter, url string) {
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, apiError{Error: err.Error()})
		return
	}
	defer resp.Body.Close()

	copyResponse(w, resp)
}

func proxyGetRaw(url string) ([]byte, int, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, http.StatusBadGateway, err
	}
	defer resp.Body.Close()
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, http.StatusBadGateway, err
	}
	return bodyBytes, resp.StatusCode, nil
}

func copyResponse(w http.ResponseWriter, resp *http.Response) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func writeJSON(w http.ResponseWriter, status int, payload interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func normalizeImageList(items []string) []string {
	if len(items) == 0 {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		trimmed := strings.TrimSpace(item)
		if trimmed == "" {
			continue
		}
		out = append(out, trimmed)
	}
	return out
}

const maxInlineImageBytes = 10 * 1024 * 1024

func probeInputImageReference(ctx context.Context, imageRef string, allowDataURL bool) error {
	trimmed := strings.TrimSpace(imageRef)
	if trimmed == "" {
		return errors.New("图片地址为空")
	}
	if strings.HasPrefix(strings.ToLower(trimmed), "data:") {
		if !allowDataURL {
			return errors.New("当前 AI_PROVIDER 不支持 data URL，请上传可访问的图片 URL")
		}
		return probeInputImageDataURL(trimmed)
	}
	return probeInputImageURL(ctx, trimmed)
}

func probeInputImageDataURL(dataURL string) error {
	raw := strings.TrimSpace(dataURL)
	if !strings.HasPrefix(strings.ToLower(raw), "data:") {
		return errors.New("data URL 格式无效")
	}

	commaIdx := strings.Index(raw, ",")
	if commaIdx <= len("data:") || commaIdx >= len(raw)-1 {
		return errors.New("data URL 缺少有效内容")
	}

	meta := strings.TrimSpace(raw[len("data:"):commaIdx])
	encoded := strings.TrimSpace(raw[commaIdx+1:])
	metaLower := strings.ToLower(meta)
	if !strings.Contains(metaLower, ";base64") {
		return errors.New("data URL 必须为 base64 编码格式")
	}

	mediaType := strings.ToLower(strings.TrimSpace(strings.Split(meta, ";")[0]))
	allowedMediaType := map[string]bool{
		"image/jpeg": true,
		"image/png":  true,
		"image/webp": true,
	}
	if !allowedMediaType[mediaType] {
		return errors.New("data URL 图片格式仅支持 jpeg/png/webp")
	}

	estimated := base64.StdEncoding.DecodedLen(len(encoded))
	if estimated <= 0 {
		return errors.New("data URL 图片内容为空")
	}
	if estimated > maxInlineImageBytes {
		return fmt.Errorf("data URL 图片不能超过 %dMB", maxInlineImageBytes/1024/1024)
	}

	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		decoded, err = base64.RawStdEncoding.DecodeString(encoded)
		if err != nil {
			return errors.New("data URL base64 内容无效")
		}
	}
	if len(decoded) == 0 {
		return errors.New("data URL 图片内容为空")
	}
	if len(decoded) > maxInlineImageBytes {
		return fmt.Errorf("data URL 图片不能超过 %dMB", maxInlineImageBytes/1024/1024)
	}

	return nil
}

func probeInputImageURL(ctx context.Context, imageURL string) error {
	parsed, err := url.Parse(strings.TrimSpace(imageURL))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return errors.New("图片 URL 格式无效")
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return errors.New("图片 URL 仅支持 http/https")
	}

	client := &http.Client{Timeout: 12 * time.Second}

	checkReq := func(method string) (*http.Response, error) {
		req, reqErr := http.NewRequestWithContext(ctx, method, imageURL, nil)
		if reqErr != nil {
			return nil, reqErr
		}
		req.Header.Set("Accept", "image/*,*/*;q=0.8")
		if method == http.MethodGet {
			req.Header.Set("Range", "bytes=0-1024")
		}
		return client.Do(req)
	}

	resp, err := checkReq(http.MethodHead)
	if err != nil {
		resp, err = checkReq(http.MethodGet)
		if err != nil {
			return fmt.Errorf("无法访问图片地址")
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusMethodNotAllowed || resp.StatusCode == http.StatusNotImplemented {
		_ = resp.Body.Close()
		resp, err = checkReq(http.MethodGet)
		if err != nil {
			return fmt.Errorf("无法访问图片地址")
		}
		defer resp.Body.Close()
	}

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusBadRequest {
		return fmt.Errorf("图片地址响应异常（HTTP %d）", resp.StatusCode)
	}

	contentType := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Type")))
	if contentType != "" && !strings.HasPrefix(contentType, "image/") {
		return fmt.Errorf("URL 返回的不是图片（Content-Type: %s）", contentType)
	}

	return nil
}

func buildPrompt(req createTaskRequest) string {
	parts := []string{
		"你是资深商业空间与招牌设计师。请为门头广告制作高质量效果图，真实还原现场比例与结构，避免透视畸变。",
	}
	if strings.TrimSpace(req.Slogan) != "" {
		parts = append(parts, "标语内容："+strings.TrimSpace(req.Slogan))
	}
	if strings.TrimSpace(req.Material) != "" {
		parts = append(parts, "字材与工艺："+strings.TrimSpace(req.Material))
	}
	if strings.TrimSpace(req.LightColor) != "" {
		parts = append(parts, "发光颜色："+strings.TrimSpace(req.LightColor))
	}
	if strings.TrimSpace(req.Board) != "" {
		parts = append(parts, "底板材质："+strings.TrimSpace(req.Board))
	}
	if strings.TrimSpace(req.Glow) != "" {
		parts = append(parts, "光效风格："+strings.TrimSpace(req.Glow))
	}
	if strings.TrimSpace(req.Details) != "" {
		parts = append(parts, "补充细节："+strings.TrimSpace(req.Details))
	}
	parts = append(parts,
		"多图规则：当输入包含多张图时，默认第 1 张是门头/现场照片，第 2 张是广告平面设计图，第 3 张及之后仅作风格参考。",
		"核心任务：提取第 2 张图中的文字内容（保持原语言、原词序、原含义），并将这些文字准确附加到第 1 张图的门头区域。",
		"文字贴合要求：严格匹配门头透视、比例、排版、字距、材质与光照，不新增与第 2 张图无关的文案。",
		"风格约束：第 3 张图作为效果风格参考图，仅迁移视觉风格（配色、质感、光影、构图），不得复制其中具体文字。",
		"执行优先级：若补充细节中包含坐标、尺寸、定位参数，必须优先按参数精确执行。",
		"风格：现代商业招牌，材质真实、细节清晰、灯光自然，画面干净有设计感。",
		"要求：标语清晰可读，边缘利落，发光与环境融合但不溢光，质感高级。",
	)
	return strings.Join(parts, "\n")
}

func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func presignUpload(filename, contentType string) (presignResponse, error) {
	accessKey := strings.TrimSpace(os.Getenv("S3_ACCESS_KEY"))
	secretKey := strings.TrimSpace(os.Getenv("S3_SECRET_KEY"))
	region := strings.TrimSpace(os.Getenv("S3_REGION"))
	bucket := strings.TrimSpace(os.Getenv("S3_BUCKET"))
	endpoint := strings.TrimSpace(os.Getenv("S3_ENDPOINT"))
	publicBase := strings.TrimRight(strings.TrimSpace(os.Getenv("S3_PUBLIC_BASE_URL")), "/")

	if accessKey == "" || secretKey == "" || region == "" || bucket == "" || publicBase == "" {
		return presignResponse{}, errors.New("S3 config missing: S3_ACCESS_KEY/S3_SECRET_KEY/S3_REGION/S3_BUCKET/S3_PUBLIC_BASE_URL")
	}

	cfg, err := config.LoadDefaultConfig(
		context.Background(),
		config.WithRegion(region),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(accessKey, secretKey, "")),
	)
	if err != nil {
		return presignResponse{}, err
	}

	if endpoint != "" {
		endpoint = normalizeS3Endpoint(endpoint, bucket)
		cfg.BaseEndpoint = aws.String(endpoint)
	}

	client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		if endpoint != "" {
			o.UsePathStyle = true
		}
	})

	key := fmt.Sprintf("uploads/%s/%s", time.Now().UTC().Format("20060102"), sanitizeFilename(filename))
	presigner := s3.NewPresignClient(client)
	putInput := &s3.PutObjectInput{
		Bucket:      aws.String(bucket),
		Key:         aws.String(key),
		ContentType: aws.String(contentType),
	}

	presigned, err := presigner.PresignPutObject(context.Background(), putInput, func(opts *s3.PresignOptions) {
		opts.Expires = 10 * time.Minute
	})
	if err != nil {
		return presignResponse{}, err
	}

	fileURL := fmt.Sprintf("%s/%s", publicBase, key)
	return presignResponse{
		UploadURL: presigned.URL,
		FileURL:   fileURL,
		Key:       key,
	}, nil
}

func normalizeS3Endpoint(rawEndpoint, bucket string) string {
	endpoint := strings.TrimRight(strings.TrimSpace(rawEndpoint), "/")
	if endpoint == "" {
		return endpoint
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return endpoint
	}
	if strings.Trim(parsed.Path, "/") == strings.Trim(bucket, "/") {
		parsed.Path = ""
		parsed.RawPath = ""
	}
	return strings.TrimRight(parsed.String(), "/")
}

func sanitizeFilename(name string) string {
	raw := strings.TrimSpace(name)
	raw = strings.ReplaceAll(raw, "\\", "/")
	raw = filepath.Base(raw)

	ext := strings.ToLower(strings.TrimSpace(filepath.Ext(raw)))
	validExt := map[string]bool{
		".jpg": true, ".jpeg": true, ".png": true, ".webp": true, ".gif": true, ".bmp": true, ".avif": true,
	}
	if !validExt[ext] {
		ext = ""
	}

	token, err := randomToken(12)
	if err != nil || strings.TrimSpace(token) == "" {
		token = fmt.Sprintf("%d", time.Now().UTC().UnixNano())
	}
	return "img_" + token + ext
}

type captchaEntry struct {
	Code    string
	Expires time.Time
}

type captchaStore struct {
	mu   sync.Mutex
	data map[string]captchaEntry
}

func newCaptchaStore() *captchaStore {
	return &captchaStore{data: make(map[string]captchaEntry)}
}

func (c *captchaStore) Generate(phone string, ttl time.Duration) string {
	code := randomDigits(6)
	c.mu.Lock()
	c.data[phone] = captchaEntry{Code: code, Expires: time.Now().Add(ttl)}
	c.mu.Unlock()
	return code
}

func (c *captchaStore) Verify(phone, code string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.data[phone]
	if !ok {
		return false
	}
	if time.Now().After(entry.Expires) {
		delete(c.data, phone)
		return false
	}
	if strings.TrimSpace(code) == "" || entry.Code != strings.TrimSpace(code) {
		return false
	}
	delete(c.data, phone)
	return true
}

func randomDigits(length int) string {
	buf := make([]byte, length)
	if _, err := rand.Read(buf); err != nil {
		return "000000"
	}
	out := make([]byte, length)
	for i := 0; i < length; i++ {
		out[i] = '0' + (buf[i] % 10)
	}
	return string(out)
}

func captchaEnabled() bool {
	return strings.ToLower(strings.TrimSpace(os.Getenv("CAPTCHA_ENABLED"))) == "true"
}
