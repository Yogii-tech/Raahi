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

// UploadFile handles file uploads by saving them to Google Cloud Storage.
func UploadFile(c *gin.Context) {
	// Limit upload size to 10MB
	if err := c.Request.ParseMultipartForm(10 << 20); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "File size exceeds limit"})
		return
	}

	fileHeader, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "No file uploaded"})
		return
	}

	// Validate file extension
	ext := strings.ToLower(filepath.Ext(fileHeader.Filename))
	if ext != ".jpg" && ext != ".jpeg" && ext != ".png" && ext != ".webp" && ext != ".pdf" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid file type. Only images and PDFs are allowed."})
		return
	}

	bucketName := os.Getenv("GCS_BUCKET_NAME")
	if bucketName == "" {
		bucketName = "raahi-uploads" // Fallback based on previous configuration
	}

	client, err := storage.NewClient(context.Background())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to connect to storage client"})
		return
	}
	defer client.Close()

	// Create a unique filename
	filename := fmt.Sprintf("%d-%s", time.Now().Unix(), filepath.Base(fileHeader.Filename))

	file, err := fileHeader.Open()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to open uploaded file"})
		return
	}
	defer file.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*50)
	defer cancel()

	wc := client.Bucket(bucketName).Object(filename).NewWriter(ctx)
	if _, err = io.Copy(wc, file); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to upload file to Cloud Storage"})
		return
	}
	if err := wc.Close(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to commit file to Cloud Storage"})
		return
	}

	url := fmt.Sprintf("https://storage.googleapis.com/%s/%s", bucketName, filename)

	c.JSON(http.StatusOK, gin.H{
		"url":      url,
		"filename": filename,
	})
}
