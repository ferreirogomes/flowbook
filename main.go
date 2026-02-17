package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"cloud.google.com/go/translate"
	"github.com/abadojack/whatlanggo"
	"github.com/gorilla/websocket"
	"github.com/ledongthuc/pdf"
	"github.com/wmentor/epub"
	"golang.org/x/text/language"
	"golang.org/x/time/rate"
	"google.golang.org/api/option"
)

// Progress message for WebSocket communication
type Progress struct {
	SessionID      string `json:"session_id"`
	Message        string `json:"message"`
	Status         string `json:"status"` // e.g., "processing", "completed", "error", "chunk_update"
	DetectedLang   string `json:"detected_lang,omitempty"`
	ChunkIndex     int    `json:"chunk_index,omitempty"`
	TotalChunks    int    `json:"total_chunks,omitempty"`
	ChunkStatus    string `json:"chunk_status,omitempty"` // "waiting", "processing", "translated"
	TranslatedText string `json:"translated_text,omitempty"`
	OriginalText   string `json:"original_text,omitempty"`
	DownloadURL    string `json:"download_url,omitempty"`
	EstimatedTime  string `json:"estimated_time,omitempty"` // New field for estimated time
}

// Job represents a file to be processed by a worker.
type Job struct {
	FilePath  string
	SessionID string
	Ext       string
}

const numWorkers = 4
const chunkBatchSize = 10                                // Process 10 chunks at a time
const simulatedChunkProcessTime = 100 * time.Millisecond // Time to process one chunk (for estimation)

var jobs = make(chan Job, 100) // Buffered channel for jobs

// Global rate limiter to respect API quotas (e.g., 10 requests/second)
var apiLimiter = rate.NewLimiter(rate.Every(100*time.Millisecond), 1)

// AppMetrics holds application metrics.
type AppMetrics struct {
	mu                sync.Mutex
	ActiveConnections int64
	JobsProcessed     int64
	JobsFailed        int64
}

var metrics AppMetrics

const (
	// Time allowed to write a message to the peer.
	writeWait = 10 * time.Second

	// Time allowed to read the next pong message from the peer.
	pongWait = 60 * time.Second

	// Send pings to peer with this period. Must be less than pongWait.
	pingPeriod = (pongWait * 9) / 10

	// Maximum message size allowed from peer.
	maxMessageSize = 512
)

// Client is a middleman between the websocket connection and the hub.
type Client struct {
	hub  *Hub
	conn *websocket.Conn
	send chan []byte
}

// readPump pumps messages from the websocket connection to the hub.
// The application runs readPump in a per-connection goroutine. The application
// ensures that there is at most one reader on a connection by executing all
// reads from this goroutine.
func (c *Client) readPump() {
	defer func() {
		c.hub.unregister <- c
		c.conn.Close()
	}()
	c.conn.SetReadLimit(maxMessageSize)
	c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error { c.conn.SetReadDeadline(time.Now().Add(pongWait)); return nil })
	for {
		_, _, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				slog.Error("websocket read error", "error", err)
			}
			break
		}
	}
}

// writePump pumps messages from the hub to the websocket connection.
func (c *Client) writePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		c.conn.Close()
	}()
	for {
		select {
		case message, ok := <-c.send:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				// The hub closed the channel.
				c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}

			w, err := c.conn.NextWriter(websocket.TextMessage)
			if err != nil {
				return
			}
			w.Write(message)

			if err := w.Close(); err != nil {
				return
			}
		case <-ticker.C:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// Hub maintains the set of active clients and broadcasts messages to the
// clients.
type Hub struct {
	clients    map[*Client]bool
	broadcast  chan Progress
	register   chan *Client
	unregister chan *Client
	mutex      sync.Mutex
}

var hub = Hub{
	broadcast:  make(chan Progress),
	register:   make(chan *Client),
	unregister: make(chan *Client),
	clients:    make(map[*Client]bool),
}

func (h *Hub) run() {
	for {
		select {
		case client := <-h.register:
			h.mutex.Lock()
			h.clients[client] = true
			h.mutex.Unlock()
			metrics.mu.Lock()
			metrics.ActiveConnections++
			metrics.mu.Unlock()
		case client := <-h.unregister:
			h.mutex.Lock()
			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
				close(client.send)
				metrics.mu.Lock()
				metrics.ActiveConnections--
				metrics.mu.Unlock()
			}
			h.mutex.Unlock()
		case progress := <-h.broadcast:
			data, err := json.Marshal(progress)
			if err != nil {
				slog.Error("json marshal error", "error", err)
				continue
			}
			h.mutex.Lock()
			for client := range h.clients {
				select {
				case client.send <- data:
				default:
					close(client.send)
					delete(h.clients, client)
				}
			}
			h.mutex.Unlock()
		}
	}
}

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true // Allow all connections
	},
}

func serveWs(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Error("websocket upgrade error", "error", err)
		return
	}
	client := &Client{hub: &hub, conn: conn, send: make(chan []byte, 256)}
	client.hub.register <- client

	// Allow collection of memory referenced by the caller by doing all work in
	// new goroutines.
	go client.writePump()
	go client.readPump()
}

func main() {
	// Setup structured logging
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	// Ensure directories exist
	os.MkdirAll("uploads", 0755)
	os.MkdirAll("outputs", 0755)

	startWorkerPool()
	go hub.run()
	http.HandleFunc("/", withLogging(homeHandler))
	http.HandleFunc("/translate", withLogging(translateHandler))
	http.HandleFunc("/download", withLogging(downloadHandler))
	http.HandleFunc("/health", withLogging(healthHandler))
	http.HandleFunc("/metrics", withLogging(metricsHandler))
	http.HandleFunc("/ws", serveWs)

	slog.Info("Starting server on :8080")
	if err := http.ListenAndServe(":8080", nil); err != nil {
		slog.Error("server failed", "error", err)
		os.Exit(1)
	}
}

func withLogging(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next(w, r)
		slog.Info("http request", "method", r.Method, "path", r.URL.Path, "remote", r.RemoteAddr, "duration", time.Since(start))
	}
}

func startWorkerPool() {
	for w := 1; w <= numWorkers; w++ {
		go worker(w, jobs)
	}
}

func worker(id int, jobs <-chan Job) {
	for job := range jobs {
		func() {
			slog.Info("worker started job", "worker_id", id, "session_id", job.SessionID)
			defer os.Remove(job.FilePath) // Clean up the temp file after processing

			var procErr error
			switch job.Ext {
			case ".pdf":
				procErr = processPDF(job.FilePath, job.SessionID)
			case ".epub":
				procErr = processEPUB(job.FilePath, job.SessionID)
			default:
				slog.Warn("unsupported file type", "ext", job.Ext, "session_id", job.SessionID)
				hub.broadcast <- Progress{SessionID: job.SessionID, Message: "Unsupported file type: " + job.Ext, Status: "error"}
			}

			if procErr != nil {
				slog.Error("error processing file", "session_id", job.SessionID, "error", procErr)
				hub.broadcast <- Progress{SessionID: job.SessionID, Message: "Error processing file: " + procErr.Error(), Status: "error"}
				metrics.mu.Lock()
				metrics.JobsFailed++
				metrics.mu.Unlock()
			} else {
				metrics.mu.Lock()
				metrics.JobsProcessed++
				metrics.mu.Unlock()
			}
			slog.Info("worker finished job", "worker_id", id, "session_id", job.SessionID)
		}()
	}
}

func homeHandler(w http.ResponseWriter, r *http.Request) {
	tmpl, err := template.ParseFiles("templates/index.html")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	tmpl.Execute(w, nil)
}

func downloadHandler(w http.ResponseWriter, r *http.Request) {
	filename := r.URL.Query().Get("file")
	if filename == "" {
		http.Error(w, "Missing file parameter", http.StatusBadRequest)
		return
	}
	// Sanitize filename to prevent directory traversal
	cleanFilename := filepath.Base(filename)
	filePath := filepath.Join("outputs", cleanFilename)
	http.ServeFile(w, r, filePath)
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("OK"))
}

func metricsHandler(w http.ResponseWriter, r *http.Request) {
	metrics.mu.Lock()
	stats := map[string]int64{
		"active_connections": metrics.ActiveConnections,
		"jobs_processed":     metrics.JobsProcessed,
		"jobs_failed":        metrics.JobsFailed,
	}
	metrics.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(stats)
}

func translateHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Only POST method is allowed", http.StatusMethodNotAllowed)
		return
	}

	file, handler, err := r.FormFile("book")
	if err != nil {
		http.Error(w, "Bad request: could not read file from form", http.StatusBadRequest)
		return
	}
	defer file.Close()

	// Create a temporary file to store the upload
	tmpfile, err := os.CreateTemp("uploads", "upload-*.tmp")
	if err != nil {
		http.Error(w, "Internal server error: could not create temporary file", http.StatusInternalServerError)
		return
	}

	if _, err = io.Copy(tmpfile, file); err != nil {
		http.Error(w, "Internal server error: could not save uploaded file", http.StatusInternalServerError)
		return
	}
	if err := tmpfile.Close(); err != nil {
		http.Error(w, "Internal server error: could not close temporary file", http.StatusInternalServerError)
		return
	}

	sessionID := handler.Filename // Use filename as a simple session ID for now

	// Create a job and send it to the worker pool
	job := Job{
		FilePath:  tmpfile.Name(),
		SessionID: sessionID,
		Ext:       filepath.Ext(handler.Filename),
	}
	jobs <- job

	// Respond to the initial HTTP request immediately
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"session_id": sessionID})
}

func processPDF(filePath string, sessionID string) error {
	hub.broadcast <- Progress{SessionID: sessionID, Message: "Processing PDF file...", Status: "processing"}

	f, r, err := pdf.Open(filePath)
	if err != nil {
		return fmt.Errorf("could not open pdf file: %w", err)
	}
	defer f.Close()

	var buf bytes.Buffer
	b, err := r.GetPlainText()
	if err != nil {
		return fmt.Errorf("could not get plain text from pdf: %w", err)
	}
	buf.ReadFrom(b)

	return processText(sessionID, buf.String())
}

func processEPUB(filePath string, sessionID string) error {
	hub.broadcast <- Progress{SessionID: sessionID, Message: "Processing EPUB file...", Status: "processing"}

	var sb strings.Builder
	if err := epub.ToTxt(filePath, &sb); err != nil {
		return fmt.Errorf("could not process epub file: %w", err)
	}

	return processText(sessionID, sb.String())
}

func processText(sessionID, text string) error {
	// 1. Identify Language
	info := whatlanggo.Detect(text)
	lang := info.Lang.String()

	// Initialize Google Translate Client
	ctx := context.Background()
	var client *translate.Client
	var err error

	if apiKey := os.Getenv("GOOGLE_API_KEY"); apiKey != "" {
		client, err = translate.NewClient(ctx, option.WithAPIKey(apiKey))
	} else {
		client, err = translate.NewClient(ctx)
	}
	if err != nil {
		return fmt.Errorf("failed to create translate client: %v", err)
	}
	defer client.Close()

	hub.broadcast <- Progress{SessionID: sessionID, Message: fmt.Sprintf("Language identified: %s", lang), Status: "processing", DetectedLang: lang}

	// 2. Split into chunks (simple split by newline or period for demo)
	chunks := strings.Split(text, "\n")
	var validChunks []string
	for _, c := range chunks {
		trimmed := strings.TrimSpace(c)
		if len(trimmed) > 0 {
			validChunks = append(validChunks, trimmed)
		}
	}

	if len(validChunks) == 0 {
		validChunks = []string{"No text content found."}
	}

	totalChunks := len(validChunks)
	// Calculate estimated time: (total chunks) * simulatedChunkProcessTime
	estimatedTime := time.Duration(totalChunks) * simulatedChunkProcessTime

	hub.broadcast <- Progress{SessionID: sessionID, Message: "Starting translation to Portuguese (Brazil)...", Status: "processing", TotalChunks: totalChunks, EstimatedTime: estimatedTime.String()}

	var translatedContent strings.Builder
	processedChunks := 0

	// 3. Process chunks in batches
	for i := 0; i < totalChunks; i += chunkBatchSize {
		end := i + chunkBatchSize
		if end > totalChunks {
			end = totalChunks
		}
		batch := validChunks[i:end]

		// Notify all chunks in the current batch as "processing"
		for j := i; j < end; j++ {
			hub.broadcast <- Progress{SessionID: sessionID, Status: "chunk_update", ChunkIndex: j, ChunkStatus: "processing"}
		}

		// Wait for rate limiter to avoid hitting API quotas
		if err := apiLimiter.Wait(ctx); err != nil {
			return fmt.Errorf("rate limiter error: %v", err)
		}

		// Perform Translation
		resp, err := client.Translate(ctx, batch, language.Make("pt-BR"), nil)
		if err != nil {
			return fmt.Errorf("translation failed: %v", err)
		}

		for j, translation := range resp {
			// Notify: Processing
			translatedChunk := translation.Text
			translatedContent.WriteString(translatedChunk + "\n")
			processedChunks++

			// Notify: Translated
			hub.broadcast <- Progress{SessionID: sessionID, Status: "chunk_update", ChunkIndex: i + j, ChunkStatus: "translated", TranslatedText: translatedChunk, OriginalText: batch[j]}
		}
	}

	// 4. Save to output file
	outputFilename := sessionID + ".txt"
	outputPath := filepath.Join("outputs", outputFilename)
	if err := os.WriteFile(outputPath, []byte(translatedContent.String()), 0644); err != nil {
		return fmt.Errorf("could not save translated file: %w", err)
	}

	// 5. Completion
	hub.broadcast <- Progress{
		SessionID:   sessionID,
		Message:     "Translation completed.",
		Status:      "completed",
		DownloadURL: "/download?file=" + outputFilename,
	}

	return nil
}
