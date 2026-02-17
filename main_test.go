package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestMain manages setup and teardown for all tests in the package.
func TestMain(m *testing.M) {
	// Create uploads directory if it doesn't exist
	if err := os.Mkdir("uploads", 0755); err != nil && !os.IsExist(err) {
		log.Fatalf("could not create uploads directory for tests: %v", err)
	}

	// Run all tests
	code := m.Run()

	// Clean up uploads directory
	if err := os.RemoveAll("uploads"); err != nil {
		log.Fatalf("could not remove uploads directory after tests: %v", err)
	}

	os.Exit(code)
}

func TestTranslateHandler(t *testing.T) {
	// Test case for non-POST methods
	t.Run("MethodNotAllowed", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/translate", nil)
		rr := httptest.NewRecorder()
		translateHandler(rr, req)

		if status := rr.Code; status != http.StatusMethodNotAllowed {
			t.Errorf("handler returned wrong status code: got %v want %v",
				status, http.StatusMethodNotAllowed)
		}

		expected := "Only POST method is allowed\n"
		if rr.Body.String() != expected {
			t.Errorf("handler returned unexpected body: got %q want %q",
				rr.Body.String(), expected)
		}
	})

	// Test case for requests without a file
	t.Run("NoFile", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/translate", nil)
		rr := httptest.NewRecorder()
		translateHandler(rr, req)

		if status := rr.Code; status != http.StatusBadRequest {
			t.Errorf("handler returned wrong status code: got %v want %v",
				status, http.StatusBadRequest)
		}

		expected := "Bad request: could not read file from form\n"
		if rr.Body.String() != expected {
			t.Errorf("handler returned unexpected body: got %q want %q",
				rr.Body.String(), expected)
		}
	})

	// Helper to create a multipart form request with a file.
	createMultipartRequest := func(filename string, filecontent string) *http.Request {
		body := &bytes.Buffer{}
		writer := multipart.NewWriter(body)
		part, err := writer.CreateFormFile("book", filename)
		if err != nil {
			t.Fatalf("CreateFormFile failed: %v", err)
		}
		if _, err = io.Copy(part, strings.NewReader(filecontent)); err != nil {
			t.Fatalf("Copy to part failed: %v", err)
		}
		if err = writer.Close(); err != nil {
			t.Fatalf("Writer close failed: %v", err)
		}

		req := httptest.NewRequest("POST", "/translate", body)
		req.Header.Set("Content-Type", writer.FormDataContentType())
		return req
	}

	// Helper to create a minimal valid PDF for testing
	createMinimalPDF := func() string {
		var buf bytes.Buffer
		buf.WriteString("%PDF-1.4\n")

		offsets := make([]int, 0)
		addObj := func(body string) {
			offsets = append(offsets, buf.Len())
			buf.WriteString(body)
		}

		addObj("1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n")
		addObj("2 0 obj\n<< /Type /Pages /Kids [ 3 0 R ] /Count 1 >>\nendobj\n")
		addObj("3 0 obj\n<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 << /Type /Font /Subtype /Type1 /BaseFont /Helvetica >> >> >> /MediaBox [ 0 0 612 792 ] /Contents 4 0 R >>\nendobj\n")

		content := "BT /F1 24 Tf 100 700 Td (Hello World) Tj ET"
		addObj(fmt.Sprintf("4 0 obj\n<< /Length %d >>\nstream\n%s\nendstream\nendobj\n", len(content), content))

		xrefOffset := buf.Len()
		buf.WriteString("xref\n0 5\n0000000000 65535 f \n")
		for _, off := range offsets {
			buf.WriteString(fmt.Sprintf("%010d 00000 n \n", off))
		}

		buf.WriteString(fmt.Sprintf("trailer\n<< /Size 5 /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF", xrefOffset))
		return buf.String()
	}

	// Test cases that involve file processing and the hub
	testCases := []struct {
		name           string
		filename       string
		fileContent    string
		expectedMsgs   []Progress
		checkMsgPrefix bool
	}{
		{
			name:        "UnsupportedFileType",
			filename:    "test.txt",
			fileContent: "this is a text file",
			expectedMsgs: []Progress{
				{SessionID: "test.txt", Message: "Unsupported file type: .txt", Status: "error"},
			},
		},
		{
			name:        "PDFProcessingError",
			filename:    "invalid.pdf",
			fileContent: "this is not a pdf",
			expectedMsgs: []Progress{
				{SessionID: "invalid.pdf", Message: "Processing PDF file...", Status: "processing"},
				{SessionID: "invalid.pdf", Message: "Error processing file: could not open pdf file:", Status: "error"},
			},
			checkMsgPrefix: true,
		},
		{
			name:        "EPUBProcessingError",
			filename:    "invalid.epub",
			fileContent: "this is not an epub",
			expectedMsgs: []Progress{
				{SessionID: "invalid.epub", Message: "Processing EPUB file...", Status: "processing"},
				{SessionID: "invalid.epub", Message: "Error processing file: could not process epub file:", Status: "error"},
			},
			checkMsgPrefix: true,
		},
		{
			name:        "PDFSuccess",
			filename:    "valid.pdf",
			fileContent: createMinimalPDF(),
			expectedMsgs: []Progress{
				{SessionID: "valid.pdf", Message: "Language identified: English", Status: "processing"},
				{SessionID: "valid.pdf", Message: "Starting translation...", Status: "processing"},
				{SessionID: "valid.pdf", Status: "chunk_update"}, // Processing
				{SessionID: "valid.pdf", Status: "completed"},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Setup a new hub for each test to avoid state leakage
			originalHub := hub
			testHub := Hub{
				broadcast:  make(chan Progress, 5), // Buffered channel
				register:   make(chan *Client),
				unregister: make(chan *Client),
				clients:    make(map[*Client]bool),
				mutex:      sync.Mutex{},
			}
			hub = testHub
			// go hub.run() // Removed to avoid race condition with test reading from broadcast channel
			defer func() { hub = originalHub }() // Restore original hub

			req := createMultipartRequest(tc.filename, tc.fileContent)
			rr := httptest.NewRecorder()
			translateHandler(rr, req)

			// Check HTTP response
			if status := rr.Code; status != http.StatusOK {
				t.Errorf("handler returned wrong status code: got %v want %v", status, http.StatusOK)
			}

			var resp map[string]string
			if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
				t.Fatalf("could not unmarshal response: %v", err)
			}
			if resp["session_id"] != tc.filename {
				t.Errorf("unexpected session_id: got %q want %q", resp["session_id"], tc.filename)
			}

			// Check messages sent to the hub
			for i, expectedMsg := range tc.expectedMsgs {
				select {
				// We might receive more messages than expected (e.g. multiple chunk updates),
				// so we loop until we find the matching expected message or timeout.
				// For this simple test update, we assume strict order for the critical status messages.
				// Note: In a real scenario, we might need a more robust event consumer here.

				case actualMsg := <-hub.broadcast:
					if actualMsg.SessionID != expectedMsg.SessionID {
						t.Errorf("msg %d: unexpected SessionID: got %q want %q", i, actualMsg.SessionID, expectedMsg.SessionID)
					}
					if actualMsg.Status != expectedMsg.Status {
						t.Errorf("msg %d: unexpected Status: got %q want %q", i, actualMsg.Status, expectedMsg.Status)
					}

					// Only check message content if it's specified in expectation
					if expectedMsg.Message != "" {
						if tc.checkMsgPrefix {
							if !strings.HasPrefix(actualMsg.Message, expectedMsg.Message) {
								t.Errorf("msg %d: unexpected Message prefix: got %q want prefix %q", i, actualMsg.Message, expectedMsg.Message)
							}
						} else if actualMsg.Message != expectedMsg.Message {
							t.Errorf("msg %d: unexpected Message: got %q want %q", i, actualMsg.Message, expectedMsg.Message)
						}
					}
				case <-time.After(2 * time.Second):
					t.Fatalf("timed out waiting for hub message %d", i)
				}
			}
		})
	}
}
