package controllers

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"cloud.google.com/go/storage"
	"github.com/gin-gonic/gin"
)

const maxUploadSize = 10 << 20 // 10MB

var allowedExtensions = map[string]bool{
	".jpg":  true,
	".jpeg": true,
	".png":  true,
	".webp": true,
	".pdf":  true,
}

// allowedMimeTypes maps detected MIME types to ensure file content matches extension.
var allowedMimeTypes = map[string]bool{
	"image/jpeg":      true,
	"image/png":       true,
	"image/webp":      true,
	"application/pdf": true,
}

// generateSafeFilename creates a cryptographically random filename with the given extension.
// Never uses any part of the user-supplied filename to prevent path traversal attacks.
func generateSafeFilename(ext string) string {
	b := make([]byte, 16) // 128-bit random
	rand.Read(b)
	return fmt.Sprintf("%d-%s%s", time.Now().UnixNano(), hex.EncodeToString(b), ext)
}

// UploadFile handles file uploads by streaming them to Google Cloud Storage (if configured)
// or falling back to local file storage.
func UploadFile(c *gin.Context) {
	if err := c.Request.ParseMultipartForm(maxUploadSize); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "File size exceeds 10MB limit"})
		return
	}

	file, header, err := c.Request.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "No file uploaded"})
		return
	}
	defer file.Close()

	// SECURITY: Validate file extension (whitelist only)
	ext := strings.ToLower(filepath.Ext(header.Filename))
	if !allowedExtensions[ext] {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid file type. Only images (jpg, jpeg, png, webp) and PDFs are allowed."})
		return
	}

	// SECURITY: Validate actual file content via magic bytes (MIME sniffing prevention).
	// Read the first 512 bytes to detect the real content type.
	sniffBuf := make([]byte, 512)
	n, _ := file.Read(sniffBuf)
	detectedType := http.DetectContentType(sniffBuf[:n])
	if !allowedMimeTypes[detectedType] {
		c.JSON(http.StatusBadRequest, gin.H{"error": "File content does not match an allowed type. Upload rejected."})
		return
	}
	// Seek back to the beginning so the full file can be copied downstream
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to process uploaded file"})
		return
	}

	// SECURITY: Generate a random filename — never use user-supplied filenames in paths
	safeFilename := generateSafeFilename(ext)

	bucketName := os.Getenv("GCS_BUCKET_NAME")
	if bucketName != "" {
		objectName := "uploads/" + safeFilename
		ctx, cancel := context.WithTimeout(c.Request.Context(), 60*time.Second)
		defer cancel()

		storageClient, err := storage.NewClient(ctx)
		if err == nil {
			wc := storageClient.Bucket(bucketName).Object(objectName).NewWriter(ctx)
			wc.ContentType = detectedType
			wc.PredefinedACL = "publicRead"

			if _, copyErr := io.Copy(wc, file); copyErr == nil {
				if closeErr := wc.Close(); closeErr == nil {
					storageClient.Close()
					fileURL := fmt.Sprintf("https://storage.googleapis.com/%s/%s", bucketName, objectName)
					c.JSON(http.StatusOK, gin.H{
						"url":      fileURL,
						"filename": objectName,
					})
					return
				}
			}
			storageClient.Close()
		}
	}

	// Local file storage fallback
	if err := os.MkdirAll("./uploads", 0750); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create local upload directory"})
		return
	}

	dstPath := filepath.Join("./uploads", safeFilename)

	out, err := os.Create(dstPath)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save file locally"})
		return
	}
	defer out.Close()

	if _, err = io.Copy(out, file); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to write file content"})
		return
	}

	scheme := "http"
	if c.Request.TLS != nil || c.GetHeader("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	fileURL := fmt.Sprintf("%s://%s/uploads/%s", scheme, c.Request.Host, safeFilename)

	c.JSON(http.StatusOK, gin.H{
		"url":      fileURL,
		"filename": safeFilename,
	})
}
