package controllers

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func createMultipartUploadRequest(t *testing.T, fieldName, filename string, content []byte) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile(fieldName, filename)
	if err != nil {
		t.Fatalf("failed to create form file: %v", err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatalf("failed to write form file content: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("failed to close multipart writer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/upload", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Host = "localhost:8080"
	return req
}

func TestUploadFile_Exceeds5MBLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("GCS_BUCKET_NAME", "")

	// PNG magic header followed by payload exceeding 5MB
	pngHeader := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}

	t.Run("rejects file slightly over 5MB (catches ParseMultipartForm disk spill bypass)", func(t *testing.T) {
		oversized := make([]byte, maxUploadSize+1024)
		copy(oversized, pngHeader)

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = createMultipartUploadRequest(t, "file", "oversized.png", oversized)

		UploadFile(c)

		if w.Code != http.StatusBadRequest {
			t.Fatalf("expected status 400 Bad Request, got %d: %s", w.Code, w.Body.String())
		}
		var resp map[string]string
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("failed to parse response JSON: %v", err)
		}
		if resp["error"] != "File size exceeds 5MB limit" {
			t.Errorf("expected error %q, got %q", "File size exceeds 5MB limit", resp["error"])
		}
	})

	t.Run("rejects large stream over 6MB via MaxBytesReader", func(t *testing.T) {
		oversized := make([]byte, (6<<20)+1024)
		copy(oversized, pngHeader)

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = createMultipartUploadRequest(t, "file", "huge.png", oversized)

		UploadFile(c)

		if w.Code != http.StatusBadRequest {
			t.Fatalf("expected status 400 Bad Request, got %d: %s", w.Code, w.Body.String())
		}
		var resp map[string]string
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("failed to parse response JSON: %v", err)
		}
		if resp["error"] != "File size exceeds 5MB limit" {
			t.Errorf("expected error %q, got %q", "File size exceeds 5MB limit", resp["error"])
		}
	})
}

func TestUploadFile_ValidSmallImageSucceeds(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("GCS_BUCKET_NAME", "")

	// Valid minimal 1x1 PNG bytes
	validPNG := []byte{
		0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A,
		0x00, 0x00, 0x00, 0x0D, 0x49, 0x48, 0x44, 0x52,
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x06, 0x00, 0x00, 0x00, 0x1F, 0x15, 0xC4,
		0x89, 0x00, 0x00, 0x00, 0x0A, 0x49, 0x44, 0x41,
		0x54, 0x78, 0x9C, 0x63, 0x00, 0x01, 0x00, 0x00,
		0x05, 0x00, 0x01, 0x0D, 0x0A, 0x2D, 0xB4, 0x00,
		0x00, 0x00, 0x00, 0x49, 0x45, 0x4E, 0x44, 0xAE,
		0x42, 0x60, 0x82,
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = createMultipartUploadRequest(t, "file", "receipt.png", validPNG)

	UploadFile(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200 OK, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response JSON: %v", err)
	}
	filename := resp["filename"]
	if filename == "" || !strings.HasSuffix(filename, ".png") {
		t.Errorf("expected generated .png filename, got %q", filename)
	}
	if resp["url"] == "" {
		t.Errorf("expected non-empty url in response")
	}

	// Clean up generated file
	_ = os.Remove(filepath.Join("./uploads", filename))
}

func TestUploadFile_DisallowedMimeTypeRejected(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("GCS_BUCKET_NAME", "")

	disguisedHTML := []byte("<!DOCTYPE html><html><body><script>alert('xss')</script></body></html>")

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = createMultipartUploadRequest(t, "file", "evil.jpg", disguisedHTML)

	UploadFile(c)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400 Bad Request for disguised HTML, got %d: %s", w.Code, w.Body.String())
	}
}
