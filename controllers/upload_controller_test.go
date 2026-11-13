package controllers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

var testValidPNG = []byte{
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

var testValidPDF = []byte("%PDF-1.4\n1 0 obj\n<<>>\nendobj\ntrailer\n<<>>\n%%EOF\n")

type uploadedRecord struct {
	bucket      string
	object      string
	contentType string
	data        []byte
}

type copyRecord struct {
	srcBucket   string
	srcObject   string
	dstBucket   string
	dstObject   string
	contentType string
}

type mockObjectStorage struct {
	mu           sync.Mutex
	uploadErr    error
	copyFailures int // fail first N CopyObject calls, then succeed
	copyErr      error
	deleteErr    error
	uploads      []uploadedRecord
	copies       []copyRecord
	deletes      []string
	copyAttempts int
}

func (m *mockObjectStorage) UploadObject(_ context.Context, bucket, object, contentType string, r io.Reader, maxBytes int64) (int64, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxBytes+1))
	if err != nil {
		return int64(len(data)), err
	}
	if int64(len(data)) > maxBytes {
		return int64(len(data)), errFileTooLarge
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.uploadErr != nil {
		return 0, m.uploadErr
	}
	m.uploads = append(m.uploads, uploadedRecord{
		bucket:      bucket,
		object:      object,
		contentType: contentType,
		data:        data,
	})
	return int64(len(data)), nil
}

func (m *mockObjectStorage) CopyObject(_ context.Context, srcBucket, srcObject, dstBucket, dstObject, contentType string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.copyAttempts++
	if m.copyFailures > 0 && m.copyAttempts <= m.copyFailures {
		if m.copyErr != nil {
			return m.copyErr
		}
		return errors.New("transient replica copy error")
	}
	if m.copyErr != nil && m.copyFailures == 0 {
		return m.copyErr
	}
	m.copies = append(m.copies, copyRecord{
		srcBucket:   srcBucket,
		srcObject:   srcObject,
		dstBucket:   dstBucket,
		dstObject:   dstObject,
		contentType: contentType,
	})
	return nil
}

func (m *mockObjectStorage) DeleteObject(_ context.Context, bucket, object string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.deleteErr != nil {
		return m.deleteErr
	}
	m.deletes = append(m.deletes, bucket+"/"+object)
	return nil
}

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
	t.Setenv("APP_ENV", "development")
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
	t.Chdir(t.TempDir())
	t.Setenv("APP_ENV", "development")
	t.Setenv("GCS_BUCKET_NAME", "")

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = createMultipartUploadRequest(t, "file", "receipt.png", testValidPNG)

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

	storedPath := filepath.Join("./uploads", filename)
	if _, err := os.Stat(storedPath); err != nil {
		t.Fatalf("expected local dev upload file at %s, got err: %v", storedPath, err)
	}
}

func TestUploadFile_DisallowedMimeTypeRejected(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("APP_ENV", "development")
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

func TestUploadFile_ProductionFailClosedWhenBucketEmpty(t *testing.T) {
	gin.SetMode(gin.TestMode)

	for _, tc := range []struct {
		name   string
		appEnv string
		bucket string
	}{
		{name: "production with empty bucket", appEnv: "production", bucket: ""},
		{name: "production with whitespace bucket", appEnv: "production", bucket: "   "},
		{name: "prod alias with empty bucket", appEnv: "prod", bucket: ""},
		{name: "staging with empty bucket", appEnv: "staging", bucket: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			t.Setenv("APP_ENV", tc.appEnv)
			t.Setenv("GCS_BUCKET_NAME", tc.bucket)
			t.Setenv("GCS_REPLICA_BUCKET_NAME", "")
			t.Setenv("GCS_REQUIRE_REPLICA", "")

			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = createMultipartUploadRequest(t, "file", "dl.png", testValidPNG)

			UploadFile(c)

			if w.Code != http.StatusServiceUnavailable {
				t.Fatalf("expected status 503 Service Unavailable, got %d: %s", w.Code, w.Body.String())
			}

			var resp map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatalf("failed to parse JSON response: %v", err)
			}
			if resp["error"] == nil || resp["error"] == "" {
				t.Errorf("expected explicit error message in response, got %v", resp)
			}
			if _, ok := resp["url"]; ok {
				t.Errorf("expected no url in fail-closed error response, got %v", resp["url"])
			}

			if _, err := os.Stat("./uploads"); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("ephemeral ./uploads directory must NOT be created in production, stat err=%v", err)
			}
		})
	}
}

func TestUploadFile_GCSPrimaryWithoutReplica(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Chdir(t.TempDir())
	t.Setenv("APP_ENV", "production")
	t.Setenv("GCS_BUCKET_NAME", "raahi-kyc-prod")
	t.Setenv("GCS_REPLICA_BUCKET_NAME", "")
	t.Setenv("GCS_REQUIRE_REPLICA", "false")

	store := &mockObjectStorage{}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = createMultipartUploadRequest(t, "file", "dl.png", testValidPNG)

	uploadFileWithDeps(c, uploadDeps{
		storage:          store,
		replicaAttempts:  3,
		replicaRetryWait: time.Millisecond,
	})

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200 OK, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse JSON response: %v", err)
	}
	filename, _ := resp["filename"].(string)
	wantURL := "https://storage.googleapis.com/raahi-kyc-prod/" + filename
	if resp["url"] != wantURL {
		t.Errorf("expected url %q, got %v", wantURL, resp["url"])
	}
	if _, hasReplicated := resp["replicated"]; hasReplicated {
		t.Errorf("expected replicated field to be omitted when no replica bucket configured, got %v", resp["replicated"])
	}

	if len(store.uploads) != 1 {
		t.Fatalf("expected 1 primary upload, got %d", len(store.uploads))
	}
	if store.uploads[0].bucket != "raahi-kyc-prod" || store.uploads[0].contentType != "image/png" {
		t.Errorf("unexpected upload record: bucket=%q contentType=%q", store.uploads[0].bucket, store.uploads[0].contentType)
	}
	if !bytes.Equal(store.uploads[0].data, testValidPNG) {
		t.Errorf("uploaded bytes mismatch primary file")
	}
	if len(store.copies) != 0 {
		t.Errorf("expected 0 replica copies when GCS_REPLICA_BUCKET_NAME is empty, got %d", len(store.copies))
	}
}

func TestUploadFile_GCSReplicationSuccessAndContentTypePreserved(t *testing.T) {
	gin.SetMode(gin.TestMode)

	for _, tc := range []struct {
		name            string
		uploadFilename  string
		payload         []byte
		wantExt         string
		wantContentType string
	}{
		{
			name:            "PNG image preserves image/png",
			uploadFilename:  "vehicle.png",
			payload:         testValidPNG,
			wantExt:         ".png",
			wantContentType: "image/png",
		},
		{
			name:            "PDF document preserves application/pdf",
			uploadFilename:  "rc.pdf",
			payload:         testValidPDF,
			wantExt:         ".pdf",
			wantContentType: "application/pdf",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("APP_ENV", "production")
			t.Setenv("GCS_BUCKET_NAME", "raahi-kyc-prod")
			t.Setenv("GCS_REPLICA_BUCKET_NAME", "raahi-kyc-replica-mumbai")
			t.Setenv("GCS_REQUIRE_REPLICA", "true")

			store := &mockObjectStorage{}
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = createMultipartUploadRequest(t, "file", tc.uploadFilename, tc.payload)

			uploadFileWithDeps(c, uploadDeps{
				storage:          store,
				replicaAttempts:  3,
				replicaRetryWait: time.Millisecond,
			})

			if w.Code != http.StatusOK {
				t.Fatalf("expected status 200 OK, got %d: %s", w.Code, w.Body.String())
			}

			var resp map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatalf("failed to parse JSON response: %v", err)
			}
			filename, _ := resp["filename"].(string)
			if !strings.HasSuffix(filename, tc.wantExt) {
				t.Errorf("expected filename suffix %q, got %q", tc.wantExt, filename)
			}
			if resp["replicated"] != true {
				t.Errorf("expected replicated=true, got %v", resp["replicated"])
			}

			if len(store.uploads) != 1 || len(store.copies) != 1 {
				t.Fatalf("expected 1 upload and 1 copy, got uploads=%d copies=%d", len(store.uploads), len(store.copies))
			}
			cp := store.copies[0]
			if cp.srcBucket != "raahi-kyc-prod" || cp.dstBucket != "raahi-kyc-replica-mumbai" || cp.srcObject != filename || cp.dstObject != filename {
				t.Errorf("unexpected copy record: %+v", cp)
			}
			if cp.contentType != tc.wantContentType {
				t.Errorf("expected replica ContentType %q, got %q", tc.wantContentType, cp.contentType)
			}
		})
	}
}

func TestUploadFile_GCSReplicaRetrySucceeds(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("APP_ENV", "production")
	t.Setenv("GCS_BUCKET_NAME", "raahi-kyc-prod")
	t.Setenv("GCS_REPLICA_BUCKET_NAME", "raahi-kyc-replica")
	t.Setenv("GCS_REQUIRE_REPLICA", "true")

	store := &mockObjectStorage{copyFailures: 2}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = createMultipartUploadRequest(t, "file", "dl.png", testValidPNG)

	uploadFileWithDeps(c, uploadDeps{
		storage:          store,
		replicaAttempts:  3,
		replicaRetryWait: time.Millisecond,
	})

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200 OK after transient replica retry, got %d: %s", w.Code, w.Body.String())
	}
	if store.copyAttempts != 3 {
		t.Errorf("expected 3 copy attempts, got %d", store.copyAttempts)
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}
	if resp["replicated"] != true {
		t.Errorf("expected replicated=true after retry recovery, got %v", resp["replicated"])
	}
}

func TestUploadFile_GCSReplicaFailure_BestEffortVsStrict(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("best-effort mode logs alert and still returns primary URL", func(t *testing.T) {
		t.Setenv("APP_ENV", "production")
		t.Setenv("GCS_BUCKET_NAME", "raahi-kyc-prod")
		t.Setenv("GCS_REPLICA_BUCKET_NAME", "raahi-kyc-replica")
		t.Setenv("GCS_REQUIRE_REPLICA", "false")

		store := &mockObjectStorage{copyErr: errors.New("replica region unavailable")}
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = createMultipartUploadRequest(t, "file", "dl.png", testValidPNG)

		uploadFileWithDeps(c, uploadDeps{
			storage:          store,
			replicaAttempts:  3,
			replicaRetryWait: time.Millisecond,
		})

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200 OK in best-effort mode, got %d: %s", w.Code, w.Body.String())
		}
		if store.copyAttempts != 3 {
			t.Errorf("expected 3 replica attempts, got %d", store.copyAttempts)
		}
		var resp map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("failed to parse JSON: %v", err)
		}
		if resp["replicated"] != false {
			t.Errorf("expected replicated=false when replica failed, got %v", resp["replicated"])
		}
		if urlStr, _ := resp["url"].(string); !strings.HasPrefix(urlStr, "https://storage.googleapis.com/raahi-kyc-prod/") {
			t.Errorf("expected valid primary GCS URL, got %q", urlStr)
		}
	})

	t.Run("strict mode fails closed with 503 when replica fails", func(t *testing.T) {
		t.Setenv("APP_ENV", "production")
		t.Setenv("GCS_BUCKET_NAME", "raahi-kyc-prod")
		t.Setenv("GCS_REPLICA_BUCKET_NAME", "raahi-kyc-replica")
		t.Setenv("GCS_REQUIRE_REPLICA", "true")

		store := &mockObjectStorage{copyErr: errors.New("permission denied on replica bucket")}
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = createMultipartUploadRequest(t, "file", "dl.png", testValidPNG)

		uploadFileWithDeps(c, uploadDeps{
			storage:          store,
			replicaAttempts:  3,
			replicaRetryWait: time.Millisecond,
		})

		if w.Code != http.StatusServiceUnavailable {
			t.Fatalf("expected 503 Service Unavailable in strict replica mode, got %d: %s", w.Code, w.Body.String())
		}
		if store.copyAttempts != 3 {
			t.Errorf("expected 3 replica attempts before failing, got %d", store.copyAttempts)
		}
		var resp map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("failed to parse JSON: %v", err)
		}
		if _, hasURL := resp["url"]; hasURL {
			t.Errorf("expected no url returned when strict replication fails, got %v", resp["url"])
		}
		if len(store.deletes) != 1 {
			t.Errorf("expected orphaned primary object to be deleted when strict replication fails, got %d deletes", len(store.deletes))
		}
	})
}

func TestUploadFile_GCSMisconfigurationAndErrors(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("GCS_REQUIRE_REPLICA=true without GCS_REPLICA_BUCKET_NAME fails with 503", func(t *testing.T) {
		t.Chdir(t.TempDir())
		t.Setenv("APP_ENV", "production")
		t.Setenv("GCS_BUCKET_NAME", "raahi-kyc-prod")
		t.Setenv("GCS_REPLICA_BUCKET_NAME", "")
		t.Setenv("GCS_REQUIRE_REPLICA", "true")

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = createMultipartUploadRequest(t, "file", "dl.png", testValidPNG)

		UploadFile(c)

		if w.Code != http.StatusServiceUnavailable {
			t.Fatalf("expected 503 Service Unavailable, got %d: %s", w.Code, w.Body.String())
		}
	})

	t.Run("same primary and replica bucket fails in strict mode and skips self-copy in best-effort", func(t *testing.T) {
		t.Setenv("APP_ENV", "production")
		t.Setenv("GCS_BUCKET_NAME", "raahi-kyc-prod")
		t.Setenv("GCS_REPLICA_BUCKET_NAME", "raahi-kyc-prod")
		t.Setenv("GCS_REQUIRE_REPLICA", "true")

		store := &mockObjectStorage{}
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = createMultipartUploadRequest(t, "file", "dl.png", testValidPNG)

		uploadFileWithDeps(c, uploadDeps{storage: store})
		if w.Code != http.StatusServiceUnavailable {
			t.Fatalf("expected 503 when same bucket + strict replica, got %d", w.Code)
		}

		t.Setenv("GCS_REQUIRE_REPLICA", "false")
		w2 := httptest.NewRecorder()
		c2, _ := gin.CreateTestContext(w2)
		c2.Request = createMultipartUploadRequest(t, "file", "dl.png", testValidPNG)

		uploadFileWithDeps(c2, uploadDeps{storage: store})
		if w2.Code != http.StatusOK {
			t.Fatalf("expected 200 in best-effort mode when skipping self-copy, got %d: %s", w2.Code, w2.Body.String())
		}
		if store.copyAttempts != 0 {
			t.Errorf("expected 0 CopyObject calls when skipping self-copy, got %d", store.copyAttempts)
		}
	})

	t.Run("GCS client init failure returns 503", func(t *testing.T) {
		t.Setenv("APP_ENV", "production")
		t.Setenv("GCS_BUCKET_NAME", "raahi-kyc-prod")
		t.Setenv("GCS_REPLICA_BUCKET_NAME", "")
		t.Setenv("GCS_REQUIRE_REPLICA", "")

		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = createMultipartUploadRequest(t, "file", "dl.png", testValidPNG)

		uploadFileWithDeps(c, uploadDeps{
			getStorage: func() (objectStorage, error) {
				return nil, errors.New("metadata server unreachable")
			},
		})
		if w.Code != http.StatusServiceUnavailable {
			t.Fatalf("expected 503 on GCS client init failure, got %d", w.Code)
		}
	})

	t.Run("GCS primary upload error returns 500 and oversized returns 400", func(t *testing.T) {
		t.Setenv("APP_ENV", "production")
		t.Setenv("GCS_BUCKET_NAME", "raahi-kyc-prod")
		t.Setenv("GCS_REPLICA_BUCKET_NAME", "")
		t.Setenv("GCS_REQUIRE_REPLICA", "")

		storeErr := &mockObjectStorage{uploadErr: errors.New("bucket quota exceeded")}
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = createMultipartUploadRequest(t, "file", "dl.png", testValidPNG)
		uploadFileWithDeps(c, uploadDeps{storage: storeErr})
		if w.Code != http.StatusInternalServerError {
			t.Fatalf("expected 500 on primary upload failure, got %d", w.Code)
		}

		storeTooLarge := &mockObjectStorage{uploadErr: errFileTooLarge}
		w2 := httptest.NewRecorder()
		c2, _ := gin.CreateTestContext(w2)
		c2.Request = createMultipartUploadRequest(t, "file", "dl.png", testValidPNG)
		uploadFileWithDeps(c2, uploadDeps{storage: storeTooLarge})
		if w2.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 on errFileTooLarge, got %d", w2.Code)
		}
	})
}

func TestValidateAndInitializeStorage(t *testing.T) {
	t.Run("development allows empty GCS_BUCKET_NAME", func(t *testing.T) {
		t.Setenv("APP_ENV", "development")
		t.Setenv("GCS_BUCKET_NAME", "")
		t.Setenv("GCS_REPLICA_BUCKET_NAME", "")
		t.Setenv("GCS_REQUIRE_REPLICA", "")

		if err := ValidateStorageConfig(); err != nil {
			t.Fatalf("expected nil validation error in dev, got %v", err)
		}
		if err := InitializeStorage(); err != nil {
			t.Fatalf("expected nil InitializeStorage in dev, got %v", err)
		}
	})

	t.Run("production rejects empty GCS_BUCKET_NAME", func(t *testing.T) {
		t.Setenv("APP_ENV", "production")
		t.Setenv("GCS_BUCKET_NAME", "")
		t.Setenv("GCS_REPLICA_BUCKET_NAME", "")
		t.Setenv("GCS_REQUIRE_REPLICA", "")

		if err := ValidateStorageConfig(); err == nil {
			t.Fatal("expected ValidateStorageConfig error in production when GCS_BUCKET_NAME is empty")
		}
		if err := InitializeStorage(); err == nil {
			t.Fatal("expected InitializeStorage error in production when GCS_BUCKET_NAME is empty")
		}
	})

	t.Run("validates replica bucket rules", func(t *testing.T) {
		t.Setenv("APP_ENV", "production")
		t.Setenv("GCS_BUCKET_NAME", "raahi-prod")
		t.Setenv("GCS_REPLICA_BUCKET_NAME", "raahi-prod-replica")
		t.Setenv("GCS_REQUIRE_REPLICA", "true")

		if err := ValidateStorageConfig(); err != nil {
			t.Fatalf("expected valid config, got %v", err)
		}

		t.Setenv("GCS_REPLICA_BUCKET_NAME", "raahi-prod")
		if err := ValidateStorageConfig(); err == nil {
			t.Fatal("expected error when replica bucket equals primary bucket")
		}
	})
}
