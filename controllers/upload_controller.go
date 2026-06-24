package controllers

import (
	"fmt"
	"net/http"
<<<<<<< HEAD
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// UploadFile handles file uploads by saving them locally to the 'uploads' directory.
func UploadFile(c *gin.Context) {
	// Limit upload size to 10MB
	if err := c.Request.ParseMultipartForm(10 << 20); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "File size exceeds limit"})
		return
	}

	file, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "No file uploaded"})
		return
	}

	// Validate file extension
	ext := strings.ToLower(filepath.Ext(file.Filename))
	if ext != ".jpg" && ext != ".jpeg" && ext != ".png" && ext != ".webp" && ext != ".pdf" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid file type. Only images and PDFs are allowed."})
		return
	}

	// Create uploads directory if it doesn't exist
	uploadDir := "uploads"
	if _, err := os.Stat(uploadDir); os.IsNotExist(err) {
		err = os.MkdirAll(uploadDir, 0755)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create upload directory"})
			return
		}
	}

	// Create a unique filename to avoid collisions
	filename := fmt.Sprintf("%d-%s", time.Now().Unix(), filepath.Base(file.Filename))
	dst := filepath.Join(uploadDir, filename)

	// Save the file to the local filesystem
	if err := c.SaveUploadedFile(file, dst); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save file locally"})
		return
	}

	// Construct the URL to access the file. 
	// We use host from the request to support both localhost and IP-based access.
	scheme := "http"
	if c.Request.TLS != nil {
		scheme = "https"
	}
	url := fmt.Sprintf("%s://%s/uploads/%s", scheme, c.Request.Host, filename)

	c.JSON(http.StatusOK, gin.H{
		"url":      url,
		"filename": filename,
	})
}
