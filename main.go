package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/gorilla/websocket"
	"github.com/ledongthuc/pdf"
	"github.com/wmentor/epub"
)

// Progress message for WebSocket communication
type Progress struct {
	SessionID string `json:"session_id"`
	Message   string `json:"message"`
	Status    string `json:"status"` // e.g., "processing", "completed", "error"
}

// Hub maintains the set of active clients and broadcasts messages to the
// clients.
type Hub struct {
	clients    map[string]*websocket.Conn
	broadcast  chan Progress
	register   chan *websocket.Conn
	unregister chan *websocket.Conn
	mutex      sync.Mutex
}

var hub = Hub{
	broadcast:  make(chan Progress),
	register:   make(chan *websocket.Conn),
	unregister: make(chan *websocket.Conn),
	clients:    make(map[string]*websocket.Conn),
}

func (h *Hub) run() {
	for {
		select {
		case conn := <-h.register:
			h.mutex.Lock()
			h.clients[conn.RemoteAddr().String()] = conn
			h.mutex.Unlock()
		case conn := <-h.unregister:
			h.mutex.Lock()
			if _, ok := h.clients[conn.RemoteAddr().String()]; ok {
				delete(h.clients, conn.RemoteAddr().String())
				if err := conn.Close(); err != nil {
					log.Printf("error closing connection: %v", err)
				}
			}
			h.mutex.Unlock()
		case progress := <-h.broadcast:
			h.mutex.Lock()
			for _, conn := range h.clients {
				// This is a simple broadcast, for a real app you'd target specific sessions
				if err := conn.WriteJSON(progress); err != nil {
					log.Printf("error: %v", err)
					h.unregister <- conn
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
		log.Println(err)
		return
	}
	hub.register <- conn
}

func main() {
	go hub.run()
	http.HandleFunc("/", homeHandler)
	http.HandleFunc("/translate", translateHandler)
	http.HandleFunc("/ws", serveWs)

	fmt.Println("Starting server on :8080")
	if err := http.ListenAndServe(":8080", nil); err != nil {
		log.Fatal(err)
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
	defer os.Remove(tmpfile.Name()) // clean up

	if _, err = io.Copy(tmpfile, file); err != nil {
		http.Error(w, "Internal server error: could not save uploaded file", http.StatusInternalServerError)
		return
	}
	if err := tmpfile.Close(); err != nil {
		http.Error(w, "Internal server error: could not close temporary file", http.StatusInternalServerError)
		return
	}

	sessionID := handler.Filename // Use filename as a simple session ID for now

	// Start processing in a goroutine
	go func() {
		ext := filepath.Ext(handler.Filename)
		var procErr error
		switch ext {
		case ".pdf":
			procErr = processPDF(tmpfile.Name(), sessionID)
		case ".epub":
			procErr = processEPUB(tmpfile.Name(), sessionID)
		default:
			hub.broadcast <- Progress{SessionID: sessionID, Message: "Unsupported file type: " + ext, Status: "error"}
			return
		}

		if procErr != nil {
			hub.broadcast <- Progress{SessionID: sessionID, Message: "Error processing file: " + procErr.Error(), Status: "error"}
			return
		}
		hub.broadcast <- Progress{SessionID: sessionID, Message: "File processed successfully.", Status: "completed"}
	}()

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

	hub.broadcast <- Progress{SessionID: sessionID, Message: "Text extracted. Translating...", Status: "processing"}
	// Placeholder for translation
	log.Printf("Extracted Text from %s:\n%s", filePath, buf.String())

	return nil
}

func processEPUB(filePath string, sessionID string) error {
	hub.broadcast <- Progress{SessionID: sessionID, Message: "Processing EPUB file...", Status: "processing"}

	var sb strings.Builder
	if err := epub.ToTxt(filePath, &sb); err != nil {
		return fmt.Errorf("could not process epub file: %w", err)
	}

	hub.broadcast <- Progress{SessionID: sessionID, Message: "Text extracted. Translating...", Status: "processing"}
	// Placeholder for translation
	log.Printf("Extracted Text from %s:\n%s", filePath, sb.String())

	return nil
}
