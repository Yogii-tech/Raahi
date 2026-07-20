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

// UploadFile handles file uploads by streaming them to Google Cloud Storage.
// Requires GCS_BUCKET_NAME and GOOGLE_APPLICATION_CREDENTIALS (or workload identity on Cloud Run).
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
	if bucketName == "" {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "File storage is not configured"})
		return
	}

	// Create a unique object name to avoid collisions
	objectName := fmt.Sprintf("uploads/%d-%s", time.Now().UnixNano(), filepath.Base(header.Filename))

	ctx, cancel := context.WithTimeout(c.Request.Context(), 60*time.Second)
	defer cancel()

	storageClient, err := storage.NewClient(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to initialize storage client"})
		return
	}
	defer storageClient.Close()

	wc := storageClient.Bucket(bucketName).Object(objectName).NewWriter(ctx)
	wc.ContentType = header.Header.Get("Content-Type")
	// Make the object publicly readable so frontend can render the URL
	wc.PredefinedACL = "publicRead"

	if _, err = io.Copy(wc, file); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to upload file"})
		return
	}

	if err = wc.Close(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to finalize upload"})
		return
	}

	// Return the public GCS URL
	fileURL := fmt.Sprintf("https://storage.googleapis.com/%s/%s", bucketName, objectName)

	c.JSON(http.StatusOK, gin.H{
		"url":      fileURL,
		"filename": objectName,
	})
}
