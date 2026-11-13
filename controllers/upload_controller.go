package controllers

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"raahi-backend/config"

	"cloud.google.com/go/storage"
	"github.com/getsentry/sentry-go"
	"github.com/gin-gonic/gin"
)

/*
Cloud Storage (GCS) Production Architecture & Replication Guide (GoRaahi)
------------------------------------------------------------------------
1. Fail-Closed Production Requirement:
   - In production (APP_ENV=production/prod/staging), GCS_BUCKET_NAME MUST be non-empty.
   - Local ./uploads/ fallback is strictly restricted to non-production development because
     Cloud Run container filesystems are ephemeral and wiped on restart/redeploy.

2. Primary Bucket (GCS_BUCKET_NAME) Recommended GCP Setup:
   - Location: Dual-region (e.g. asia-south1 + asia-south2) or Multi-region (asia) for
     regional high availability and India data residency near Cloud Run (asia-south2).
   - Access Control: Uniform Bucket-Level Access enabled; public object reads via IAM
     binding `allUsers -> roles/storage.objectViewer` (never per-object PredefinedACL).
   - Data Protection: Enable Object Versioning (`gcloud storage buckets update gs://<PRIMARY> --versioning`)
     with a Lifecycle rule (e.g. delete noncurrent versions after 90 days / keep 3 versions)
     plus GCS Soft Delete (7-90 days) to recover from accidental deletions.

3. Secondary Replica / Backup Bucket (GCS_REPLICA_BUCKET_NAME) Recommended GCP Setup:
   - Create a dedicated private backup bucket in a separate region (e.g. asia-south1 if
     primary is asia-south2, or asia multi-region) WITHOUT `allUsers` public access.
   - Enable Object Versioning + a Bucket Retention Policy (30-365 days; lock only after
     validating retention period) for immutable KYC/compliance backup.
   - Cloud Run Service Account IAM:
     * Primary bucket: roles/storage.objectViewer + roles/storage.objectCreator
     * Replica bucket: roles/storage.objectCreator (needed by server-side CopierFrom)
   - Set GCS_REQUIRE_REPLICA=true to fail uploads (HTTP 503) if replication fails after
     retries; default (false) logs a [Upload][ALERT] + Sentry event while keeping primary
     uploads available during backup bucket/region incidents.
   - Use GCS Storage Transfer Service to backfill pre-existing objects to the replica bucket.
*/

const (
	maxUploadSize           = 5 << 20 // 5MB
	defaultGCSUploadTimeout = 60 * time.Second
	defaultGCSCopyTimeout   = 30 * time.Second
	defaultReplicaAttempts  = 3
	defaultReplicaRetryWait = 150 * time.Millisecond
)

var errFileTooLarge = errors.New("file size exceeds 5MB limit")

var (
	gcsClientMu sync.Mutex
	gcsClient   *storage.Client
)

// objectStorage abstracts GCS upload and server-side copy operations so UploadFile
// can be unit-tested thoroughly without requiring live GCP credentials.
type objectStorage interface {
	UploadObject(ctx context.Context, bucket, object, contentType string, r io.Reader, maxBytes int64) (int64, error)
	CopyObject(ctx context.Context, srcBucket, srcObject, dstBucket, dstObject, contentType string) error
	DeleteObject(ctx context.Context, bucket, object string) error
}

type gcsObjectStorage struct {
	client *storage.Client
}

func (s *gcsObjectStorage) UploadObject(ctx context.Context, bucket, object, contentType string, r io.Reader, maxBytes int64) (int64, error) {
	uploadCtx, cancel := context.WithTimeout(ctx, defaultGCSUploadTimeout)
	defer cancel()

	wc := s.client.Bucket(bucket).Object(object).NewWriter(uploadCtx)
	wc.ContentType = contentType
	// NOTE: Do NOT set wc.PredefinedACL here. The bucket has Uniform Bucket-Level
	// Access enabled, which means per-object ACLs are silently ignored by GCS.
	// Public read access is granted at the bucket level via the IAM binding:
	//   allUsers → roles/storage.objectViewer
	// To change visibility, update the bucket IAM policy — not per-object ACLs.

	written, err := io.Copy(wc, io.LimitReader(r, maxBytes+1))
	if err != nil {
		// Cancel uploadCtx before Close so GCS aborts the partial upload instead of committing it.
		cancel()
		_ = wc.Close()
		return written, fmt.Errorf("stream copy: %w", err)
	}
	if written > maxBytes {
		cancel()
		_ = wc.Close()
		return written, errFileTooLarge
	}
	if err := wc.Close(); err != nil {
		return written, fmt.Errorf("writer close: %w", err)
	}
	return written, nil
}

func (s *gcsObjectStorage) CopyObject(ctx context.Context, srcBucket, srcObject, dstBucket, dstObject, contentType string) error {
	copyCtx, cancel := context.WithTimeout(ctx, defaultGCSCopyTimeout)
	defer cancel()

	src := s.client.Bucket(srcBucket).Object(srcObject)
	dst := s.client.Bucket(dstBucket).Object(dstObject)
	copier := dst.CopierFrom(src)
	copier.ContentType = contentType
	// NOTE: Do NOT set copier.PredefinedACL; backup buckets use Uniform Bucket-Level Access.
	if _, err := copier.Run(copyCtx); err != nil {
		return err
	}
	return nil
}

func (s *gcsObjectStorage) DeleteObject(ctx context.Context, bucket, object string) error {
	delCtx, cancel := context.WithTimeout(ctx, defaultGCSCopyTimeout)
	defer cancel()
	return s.client.Bucket(bucket).Object(object).Delete(delCtx)
}

// getSharedGCSClient returns a lazily initialized, package-level *storage.Client.
// Reusing a single client enables HTTP/2/TLS connection pooling across requests.
// Uses context.Background() because oauth2 token sources bind to the init context
// for future credential refreshes.
func getSharedGCSClient() (*storage.Client, error) {
	gcsClientMu.Lock()
	defer gcsClientMu.Unlock()

	if gcsClient != nil {
		return gcsClient, nil
	}

	client, err := storage.NewClient(context.Background())
	if err != nil {
		return nil, err
	}
	gcsClient = client
	return gcsClient, nil
}

// CloseStorage closes the shared GCS client during graceful server shutdown.
func CloseStorage() error {
	gcsClientMu.Lock()
	defer gcsClientMu.Unlock()

	if gcsClient == nil {
		return nil
	}
	err := gcsClient.Close()
	gcsClient = nil
	return err
}

type storageConfig struct {
	appEnv         string
	isProd         bool
	bucketName     string
	replicaBucket  string
	requireReplica bool
}

func isProductionAppEnv(appEnv string) bool {
	return config.IsProductionEnv(appEnv)
}

func parseEnvBool(val string) bool {
	switch strings.ToLower(strings.TrimSpace(val)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func loadStorageConfigFromEnv() storageConfig {
	appEnv := strings.TrimSpace(os.Getenv("APP_ENV"))
	return storageConfig{
		appEnv:         appEnv,
		isProd:         isProductionAppEnv(appEnv),
		bucketName:     strings.TrimSpace(os.Getenv("GCS_BUCKET_NAME")),
		replicaBucket:  strings.TrimSpace(os.Getenv("GCS_REPLICA_BUCKET_NAME")),
		requireReplica: parseEnvBool(os.Getenv("GCS_REQUIRE_REPLICA")),
	}
}

func validateStorageConfig(cfg storageConfig) error {
	if cfg.isProd && cfg.bucketName == "" {
		return fmt.Errorf("GCS_BUCKET_NAME must be set when APP_ENV=%q (ephemeral ./uploads fallback is disabled in production)", cfg.appEnv)
	}
	if cfg.requireReplica && cfg.replicaBucket == "" {
		return errors.New("GCS_REQUIRE_REPLICA=true requires GCS_REPLICA_BUCKET_NAME to be set")
	}
	if cfg.bucketName != "" && cfg.replicaBucket != "" && cfg.replicaBucket == cfg.bucketName {
		return fmt.Errorf("GCS_REPLICA_BUCKET_NAME (%q) must differ from GCS_BUCKET_NAME", cfg.replicaBucket)
	}
	return nil
}

// ValidateStorageConfig validates GCS storage and replication environment configuration.
func ValidateStorageConfig() error {
	return validateStorageConfig(loadStorageConfigFromEnv())
}

// InitializeStorage validates GCS configuration and warms up the shared GCS client at startup.
func InitializeStorage() error {
	cfg := loadStorageConfigFromEnv()
	if err := validateStorageConfig(cfg); err != nil {
		return err
	}
	if cfg.bucketName == "" {
		log.Printf("[Storage][INFO] GCS_BUCKET_NAME not set (APP_ENV=%q) — local ./uploads fallback enabled for development only", cfg.appEnv)
		return nil
	}
	if _, err := getSharedGCSClient(); err != nil {
		return fmt.Errorf("failed to initialize shared GCS client: %w", err)
	}
	if cfg.replicaBucket == "" {
		log.Printf("[Storage][WARN] GCS primary bucket=%q configured without GCS_REPLICA_BUCKET_NAME — configure a cross-region replica bucket or dual/multi-region bucket with Object Versioning", cfg.bucketName)
	} else {
		log.Printf("[Storage] GCS configured: primary=%q replica=%q requireReplica=%v", cfg.bucketName, cfg.replicaBucket, cfg.requireReplica)
	}
	return nil
}

type uploadDeps struct {
	storage          objectStorage
	getStorage       func() (objectStorage, error)
	replicaAttempts  int
	replicaRetryWait time.Duration
}

func defaultUploadDeps() uploadDeps {
	return uploadDeps{
		getStorage: func() (objectStorage, error) {
			client, err := getSharedGCSClient()
			if err != nil {
				return nil, err
			}
			return &gcsObjectStorage{client: client}, nil
		},
		replicaAttempts:  defaultReplicaAttempts,
		replicaRetryWait: defaultReplicaRetryWait,
	}
}

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

// mimeToExtension maps each allowed detected MIME type to the extension used for storage.
var mimeToExtension = map[string]string{
	"image/jpeg":      ".jpg",
	"image/png":       ".png",
	"image/webp":      ".webp",
	"application/pdf": ".pdf",
}

// generateSafeFilename creates a cryptographically random filename with the given extension.
// Never uses any part of the user-supplied filename to prevent path traversal attacks.
func generateSafeFilename(ext string) string {
	b := make([]byte, 16) // 128-bit random
	_, _ = rand.Read(b)
	return fmt.Sprintf("%d-%s%s", time.Now().UnixNano(), hex.EncodeToString(b), ext)
}

func replicateWithRetry(
	ctx context.Context,
	store objectStorage,
	srcBucket, srcObject, dstBucket, dstObject, contentType string,
	maxAttempts int,
	baseDelay time.Duration,
) error {
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if attempt > 1 && baseDelay > 0 {
			backoff := baseDelay * time.Duration(1<<(attempt-2))
			timer := time.NewTimer(backoff)
			select {
			case <-ctx.Done():
				timer.Stop()
				if lastErr != nil {
					return fmt.Errorf("%w (retry aborted: %v)", lastErr, ctx.Err())
				}
				return ctx.Err()
			case <-timer.C:
			}
		}

		err := store.CopyObject(ctx, srcBucket, srcObject, dstBucket, dstObject, contentType)
		if err == nil {
			if attempt > 1 {
				log.Printf("[Upload] GCS replica copy succeeded on attempt %d/%d (gs://%s/%s -> gs://%s/%s)", attempt, maxAttempts, srcBucket, srcObject, dstBucket, dstObject)
			}
			return nil
		}
		lastErr = err
		log.Printf("[Upload][WARN] GCS replica copy attempt %d/%d failed (gs://%s/%s -> gs://%s/%s): %v", attempt, maxAttempts, srcBucket, srcObject, dstBucket, dstObject, err)

		if ctx.Err() != nil {
			break
		}
	}
	return lastErr
}

// UploadFile handles file uploads by streaming them to Google Cloud Storage (with optional
// cross-bucket replication via GCS_REPLICA_BUCKET_NAME), or falling back to local disk
// in non-production development environments only.
func UploadFile(c *gin.Context) {
	uploadFileWithDeps(c, defaultUploadDeps())
}

func uploadFileWithDeps(c *gin.Context, deps uploadDeps) {
	cfg := loadStorageConfigFromEnv()

	// SECURITY / RELIABILITY: Fail closed in production if GCS_BUCKET_NAME is empty
	// or strict replication is misconfigured. Never store production KYC documents or
	// images on ephemeral Cloud Run container disk (./uploads/).
	if cfg.isProd && cfg.bucketName == "" {
		msg := fmt.Sprintf("[Upload][CRITICAL] Refusing upload in APP_ENV=%q: GCS_BUCKET_NAME is empty (ephemeral ./uploads fallback disabled in production)", cfg.appEnv)
		log.Println(msg)
		sentry.CaptureMessage(msg)
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Storage service is not configured. Please contact support."})
		return
	}
	if cfg.requireReplica && cfg.replicaBucket == "" {
		msg := "[Upload][CRITICAL] Refusing upload: GCS_REQUIRE_REPLICA=true but GCS_REPLICA_BUCKET_NAME is empty"
		log.Println(msg)
		sentry.CaptureMessage(msg)
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Backup storage service is not configured. Please contact support."})
		return
	}
	if cfg.bucketName != "" && cfg.replicaBucket == cfg.bucketName {
		msg := fmt.Sprintf("[Upload][ERROR] GCS_REPLICA_BUCKET_NAME (%q) equals GCS_BUCKET_NAME", cfg.replicaBucket)
		log.Println(msg)
		if cfg.requireReplica {
			sentry.CaptureMessage(msg)
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Backup storage is misconfigured. Please contact support."})
			return
		}
		// Skip self-copy in best-effort mode so we do not overwrite the primary object with itself.
		cfg.replicaBucket = ""
	}

	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxUploadSize+64*1024)
	if err := c.Request.ParseMultipartForm(maxUploadSize); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "File size exceeds 5MB limit"})
		return
	}

	file, header, err := c.Request.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "No file uploaded"})
		return
	}
	defer file.Close()

	if header.Size > maxUploadSize {
		c.JSON(http.StatusBadRequest, gin.H{"error": "File size exceeds 5MB limit"})
		return
	}

	// SECURITY: Validate actual file content via magic bytes (MIME sniffing prevention).
	// Read the first 512 bytes to detect the real content type.
	sniffBuf := make([]byte, 512)
	n, _ := io.ReadFull(file, sniffBuf)
	detectedType := http.DetectContentType(sniffBuf[:n])
	if !allowedMimeTypes[detectedType] {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Unsupported file. Please upload a JPG, PNG, WebP image or a PDF (HEIC/HEIF photos are not supported)."})
		return
	}

	// SECURITY: The stored extension is derived from the sniffed content type, never from
	// the client-supplied filename. Mobile gallery pickers frequently supply files with no
	// extension or an unexpected one (e.g. ".jpe", ".image"), which must not block valid uploads.
	ext := mimeToExtension[detectedType]
	if userExt := strings.ToLower(filepath.Ext(header.Filename)); userExt != "" && !allowedExtensions[userExt] {
		log.Printf("[Upload] Accepting file with unrecognised extension %q (content type %s)", userExt, detectedType)
	}
	// Seek back to the beginning so the full file can be copied downstream
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to process uploaded file"})
		return
	}

	// SECURITY: Generate a random filename — never use user-supplied filenames in paths
	safeFilename := generateSafeFilename(ext)

	if cfg.bucketName != "" {
		// GCS is configured — never fall back to local disk here: Cloud Run containers
		// are ephemeral and any file saved to ./uploads/ will be lost on restart/deploy.
		// Objects are stored at the bucket root (no subdirectory prefix) to match
		// the existing files already in the bucket and their stored MongoDB URLs.
		objectName := safeFilename

		store := deps.storage
		if store == nil {
			var storeErr error
			store, storeErr = deps.getStorage()
			if storeErr != nil {
				log.Printf("[Upload] GCS client init failed: %v", storeErr)
				c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Storage service unavailable. Please try again."})
				return
			}
		}

		reqCtx := c.Request.Context()
		if _, err := store.UploadObject(reqCtx, cfg.bucketName, objectName, detectedType, file, maxUploadSize); err != nil {
			if errors.Is(err, errFileTooLarge) {
				c.JSON(http.StatusBadRequest, gin.H{"error": "File size exceeds 5MB limit"})
				return
			}
			log.Printf("[Upload] GCS primary upload failed (bucket=%s object=%s): %v", cfg.bucketName, objectName, err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to upload file to storage. Please try again."})
			return
		}

		replicated := false
		if cfg.replicaBucket != "" {
			attempts := deps.replicaAttempts
			if attempts <= 0 {
				attempts = defaultReplicaAttempts
			}
			retryWait := deps.replicaRetryWait
			if retryWait < 0 {
				retryWait = 0
			}
			repErr := replicateWithRetry(
				reqCtx,
				store,
				cfg.bucketName,
				objectName,
				cfg.replicaBucket,
				objectName,
				detectedType,
				attempts,
				retryWait,
			)
			if repErr != nil {
				if cfg.requireReplica {
					delCtx, delCancel := context.WithTimeout(context.Background(), defaultGCSCopyTimeout)
					if delErr := store.DeleteObject(delCtx, cfg.bucketName, objectName); delErr != nil {
						log.Printf("[Upload][WARN] Failed to delete orphaned primary GCS object gs://%s/%s after strict replica failure: %v", cfg.bucketName, objectName, delErr)
					}
					delCancel()
					msg := fmt.Sprintf("[Upload][ERROR] GCS replica copy failed after %d attempts and GCS_REQUIRE_REPLICA=true (src=gs://%s/%s dst=gs://%s/%s): %v", attempts, cfg.bucketName, objectName, cfg.replicaBucket, objectName, repErr)
					log.Println(msg)
					sentry.CaptureMessage(msg)
					c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Failed to replicate file to backup storage. Please try again."})
					return
				}
				alertMsg := fmt.Sprintf("[Upload][ALERT] GCS replica copy failed after %d attempts (continuing with GCS_REQUIRE_REPLICA=false; src=gs://%s/%s dst=gs://%s/%s): %v", attempts, cfg.bucketName, objectName, cfg.replicaBucket, objectName, repErr)
				log.Println(alertMsg)
				sentry.CaptureMessage(alertMsg)
			} else {
				replicated = true
			}
		}

		fileURL := fmt.Sprintf("https://storage.googleapis.com/%s/%s", cfg.bucketName, objectName)
		resp := gin.H{
			"url":      fileURL,
			"filename": objectName,
		}
		if cfg.replicaBucket != "" {
			resp["replicated"] = replicated
		}
		c.JSON(http.StatusOK, resp)
		return
	}

	// Defense-in-depth: never enter local disk fallback in production environments.
	if cfg.isProd {
		log.Printf("[Upload][CRITICAL] Blocked ephemeral local disk write in production environment (%s)", cfg.appEnv)
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Storage service is not configured. Please contact support."})
		return
	}

	if cfg.replicaBucket != "" {
		log.Printf("[Upload][WARN] GCS_REPLICA_BUCKET_NAME=%q is set while GCS_BUCKET_NAME is empty — skipping GCS replication in local dev mode", cfg.replicaBucket)
	}

	// Local file storage fallback (development only — GCS_BUCKET_NAME not set)
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

	written, err := io.Copy(out, io.LimitReader(file, maxUploadSize+1))
	if err != nil {
		_ = out.Close()
		_ = os.Remove(dstPath)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to write file content"})
		return
	}
	if written > maxUploadSize {
		_ = out.Close()
		_ = os.Remove(dstPath)
		c.JSON(http.StatusBadRequest, gin.H{"error": "File size exceeds 5MB limit"})
		return
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(dstPath)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to close local file"})
		return
	}

	scheme := "https"
	fwdProto := strings.ToLower(strings.TrimSpace(strings.Split(c.GetHeader("X-Forwarded-Proto"), ",")[0]))
	host := c.Request.Host
	isLocal := strings.HasPrefix(host, "localhost") || strings.HasPrefix(host, "127.0.0.1")
	if c.Request.TLS == nil && fwdProto != "https" && isLocal {
		scheme = "http"
	}
	fileURL := fmt.Sprintf("%s://%s/uploads/%s", scheme, c.Request.Host, safeFilename)

	c.JSON(http.StatusOK, gin.H{
		"url":      fileURL,
		"filename": safeFilename,
	})
}
