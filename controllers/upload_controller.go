package controllers

import (
	"context"
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

	// Validate file extension
	ext := strings.ToLower(filepath.Ext(header.Filename))
	if !allowedExtensions[ext] {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid file type. Only images (jpg, jpeg, png, webp) and PDFs are allowed."})
		return
	}

	bucketName := os.Getenv("GCS_BUCKET_NAME")
	if bucketName != "" {
		objectName := fmt.Sprintf("uploads/%d-%s", time.Now().UnixNano(), filepath.Base(header.Filename))
		ctx, cancel := context.WithTimeout(c.Request.Context(), 60*time.Second)
		defer cancel()

		storageClient, err := storage.NewClient(ctx)
		if err == nil {
			wc := storageClient.Bucket(bucketName).Object(objectName).NewWriter(ctx)
			wc.ContentType = header.Header.Get("Content-Type")
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
	if err := os.MkdirAll("./uploads", os.ModePerm); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create local upload directory"})
		return
	}

	filename := fmt.Sprintf("%d-%s", time.Now().UnixNano(), filepath.Base(header.Filename))
	dstPath := filepath.Join("./uploads", filename)

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
	fileURL := fmt.Sprintf("%s://%s/uploads/%s", scheme, c.Request.Host, filename)

	c.JSON(http.StatusOK, gin.H{
		"url":      fileURL,
		"filename": filename,
	})
}
