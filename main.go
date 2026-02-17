package main

import (
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"github.com/wmentor/epub"
	"strings"
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
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	defer file.Close()

	// Create the uploads directory if it doesn't exist
	if _, err := os.Stat("uploads"); os.IsNotExist(err) {
		os.Mkdir("uploads", os.ModePerm)
	}

	// Create a new file in the uploads directory
	dst, err := os.Create(filepath.Join("uploads", handler.Filename))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer dst.Close()

	// Copy the uploaded file to the destination file
	if _, err := io.Copy(dst, file); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Process the file based on its type
	ext := filepath.Ext(handler.Filename)
	switch ext {
	case ".pdf":
		processPDF(dst.Name())
	case ".epub":
		processEPUB(dst.Name())
	default:
		http.Error(w, "Unsupported file type", http.StatusBadRequest)
		return
	}

	fmt.Fprintf(w, "File '%s' uploaded successfully and is being processed.", handler.Filename)
}

func processPDF(filePath string) {
	// Placeholder for PDF processing logic
	log.Printf("Processing PDF file: %s", filePath)
}

func processEPUB(filePath string) {
	log.Printf("Processing EPUB file: %s", filePath)

	book, err := epub.Open(filePath)
	if err != nil {
		log.Printf("Error opening EPUB file: %v", err)
		return
	}
	defer book.Close()

	for cur := book.Nav.Get(0); cur != nil; cur = book.Nav.Next() {
		if !cur.IsChapter() {
			continue
		}

		r, err := cur.Open()
		if err != nil {
			log.Printf("Error opening chapter: %v", err)
			continue
		}
		defer r.Close()

		sb := new(strings.Builder)
		if _, err := io.Copy(sb, r); err != nil {
			log.Printf("Error reading chapter content: %v", err)
			continue
		}

		// For now, just log the plain text of the chapter
		plainText, err := book.GetPlainText(sb.String())
		if err != nil {
			log.Printf("Error getting plain text: %v", err)
			continue
		}
		log.Printf("Extracted Text: %s", plainText)
	}
}
