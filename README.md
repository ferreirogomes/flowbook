# Flowbook Translator

> *Honoring the author's distinct voice through sophisticated neural cross-lingual mapping.*

Flowbook is a Go-based web application designed to translate PDF and EPUB manuscripts into **Portuguese (Brazil)** while preserving the reading experience. It leverages the **Google Cloud Translation API** for high-quality neural translations and features a classic, parchment-styled user interface.

![License](https://img.shields.io/badge/license-MIT-blue.svg)
![Go Version](https://img.shields.io/badge/Go-1.24+-00ADD8?logo=go)

## ✨ Features

-   **Format Support**: Seamlessly parses and processes **PDF** and **EPUB** files.
-   **Real-time Progress**: Uses WebSockets to stream translation status, including language detection, chunk processing, and estimated completion time.
-   **Live Preview**: View the first few translated chunks side-by-side with the original text before the full download is ready.
-   **Concurrent Processing**: Implements a worker pool pattern to handle multiple file uploads efficiently without overloading the server.
-   **Rate Limiting**: Built-in rate limiter to respect Google Cloud API quotas and prevent resource exhaustion.
-   **Classic UI**: A beautiful, responsive frontend styled with serif typography and parchment textures to mimic the feel of a classic book.

## 🛠️ Tech Stack

-   **Backend**: Go (Golang)
-   **Frontend**: HTML5, CSS3 (Crimson Text font), JavaScript (WebSockets)
-   **Translation Engine**: Google Cloud Translation API
-   **Libraries**:
    -   `github.com/gorilla/websocket`: Real-time communication.
    -   `github.com/ledongthuc/pdf`: PDF text extraction.
    -   `github.com/wmentor/epub`: EPUB text extraction.
    -   `github.com/abadojack/whatlanggo`: Language detection.

## 🚀 Getting Started

### Prerequisites

1.  **Go**: Ensure you have Go 1.24 or later installed.
2.  **Google Cloud Project**:
    -   Enable the **Cloud Translation API**.
    -   Obtain an API Key OR a Service Account JSON key.

### Installation

1.  **Clone the repository**:
    ```bash
    git clone https://github.com/yourusername/flowbook.git
    cd flowbook
    ```

2.  **Install dependencies**:
    ```bash
    go mod tidy
    ```

### Configuration

You can authenticate with Google Cloud in two ways:

**Option A: API Key (Simplest)**
Set the `GOOGLE_API_KEY` environment variable.
```bash
export GOOGLE_API_KEY="your_api_key_here"
```

**Option B: Service Account (Recommended for Production)**
Set the `GOOGLE_APPLICATION_CREDENTIALS` environment variable to the path of your JSON key file.
```bash
export GOOGLE_APPLICATION_CREDENTIALS="/path/to/service-account-key.json"
```

### Running the Application

Start the server:
```bash
go run main.go
```

The application will start on port `8080`.

## 📖 Usage

1.  Open your browser and navigate to `http://localhost:8080`.
2.  **Drag and drop** a PDF or EPUB file onto the designated area, or click to select a file.
3.  Watch the **real-time status** as the file is uploaded, the language is detected, and the text is chunked.
4.  Observe the **Translation Preview** to see the first few paragraphs translated instantly.
5.  Once completed, click the **Download Translated File** button to get your translated text file (`.txt`).

## 📂 Project Structure

```
flowbook/
├── main.go             # Main application logic (Server, WebSocket Hub, Worker Pool)
├── main_test.go        # Unit tests
├── go.mod              # Go module definition
├── go.sum              # Dependency checksums
├── templates/
│   └── index.html      # Frontend UI
├── uploads/            # Temporary storage for uploaded files
└── outputs/            # Storage for translated files
```

## 📄 License

This project is open-source and available under the MIT License.