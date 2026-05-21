package controllers

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"cloud.google.com/go/storage"
	"github.com/gin-gonic/gin"
)

const bucketName = "raahi-documents-137804375265"

func UploadFile(c *gin.Context) {
	file, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "No file uploaded"})
		return
	}

	ctx := context.Background()
	client, err := storage.NewClient(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to connect to storage"})
		return
	}
	defer client.Close()

	// Create a unique filename
	filename := fmt.Sprintf("%d-%s", time.Now().Unix(), file.Filename)
	bucket := client.Bucket(bucketName)
	obj := bucket.Object(filename)

	// Open the uploaded file
	src, err := file.Open()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to open file"})
		return
	}
	defer src.Close()

	// Write to GCS
	wc := obj.NewWriter(ctx)
	if _, err = io.Copy(wc, src); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to upload to storage"})
		return
	}
	if err := wc.Close(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to finalize upload"})
		return
	}

	// Double check ACL: Ensure the object is publicly readable
	// (Even though we set bucket level, this is safer)
	if err := obj.ACL().Set(ctx, storage.AllUsers, storage.RoleReader); err != nil {
        // Log error but don't fail, bucket policy might already handle it
        fmt.Printf("Warning: Failed to set ACL on %s: %v\n", filename, err)
    }

	// Return the public URL
	url := fmt.Sprintf("https://storage.googleapis.com/%s/%s", bucketName, filename)
	c.JSON(http.StatusOK, gin.H{"url": url})
}
