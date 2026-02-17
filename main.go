package main

import (
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/wmentor/epub"
)

func main() {
	http.HandleFunc("/", homeHandler)
	http.HandleFunc("/translate", translateHandler)

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


	// Process the file based on its type
	ext := filepath.Ext(handler.Filename)
	var procErr error
	switch ext {
	case ".pdf":
		procErr = processPDF(tmpfile.Name())
	case ".epub":
		procErr = processEPUB(tmpfile.Name())
	default:
		http.Error(w, "Unsupported file type: "+ext, http.StatusBadRequest)
		return
	}

	if procErr != nil {
		http.Error(w, "Error processing file: "+procErr.Error(), http.StatusInternalServerError)
		return
	}

	fmt.Fprintf(w, "File '%s' uploaded and processed successfully.", handler.Filename)
}

func processPDF(filePath string) error {
	// Placeholder for PDF processing logic
	log.Printf("Processing PDF file: %s", filePath)
	return nil
}

func processEPUB(filePath string) error {
	log.Printf("Processing EPUB file: %s", filePath)

	var sb strings.Builder
	if err := epub.ToTxt(filePath, &sb); err != nil {
		log.Printf("Error processing EPUB file %s: %v", filePath, err)
		return fmt.Errorf("could not process epub file: %w", err)
	}

	// For now, just log the extracted text.
	// In the future, this text will be sent for translation.
	log.Printf("Extracted Text from %s:\n%s", filePath, sb.String())

	return nil
}

