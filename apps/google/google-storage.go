package google

import (
	"bytes"
	"context"
	"io"
	"time"

	"github.com/labstack/echo/v4"
	"google.golang.org/api/option"
	"google.golang.org/api/storage/v1"

	"github.com/StorX2-0/Backup-Tools/pkg/prometheus"
)

type StorageClient struct {
	*storage.Service
}

func NewGoogleStorageClient(c echo.Context) (*StorageClient, error) {
	start := time.Now()
	defer func() {
		prometheus.RecordOperation("storage_client_creation", "success", time.Since(start), "service", "storage")
	}()

	client, err := client(c)
	if err != nil {
		prometheus.RecordError("storage_auth_error", "get_client", "service", "storage")
		return nil, err
	}

	srv, err := storage.NewService(context.Background(), option.WithHTTPClient(client))
	if err != nil {
		prometheus.RecordError("storage_service_error", "create_service", "service", "storage")
		return nil, err
	}
	return &StorageClient{srv}, nil
}

// Takes project name and returns JSON list of all buckets in this project.
func (client *StorageClient) ListBucketsJSON(c echo.Context, projectName string) (*storage.Buckets, error) {
	start := time.Now()
	defer func() {
		prometheus.RecordOperation("storage_list_buckets", "success", time.Since(start), "service", "storage")
	}()

	bucketList, err := client.Buckets.List(projectName).Do()
	if err != nil {
		prometheus.RecordError("storage_api_error", "list_buckets", "service", "storage")
		return nil, err
	}
	/*listJSON, err := bucketList.MarshalJSON()
	if err != nil {
		return "", err
	}*/
	prometheus.RecordCounter("storage_buckets_retrieved", int64(len(bucketList.Items)), "service", "storage")
	return bucketList, nil
}

func (client *StorageClient) GetBucket(c echo.Context, bucketName string) (*storage.Bucket, error) {
	start := time.Now()
	defer func() {
		prometheus.RecordOperation("storage_get_bucket", "success", time.Since(start), "service", "storage")
	}()

	bucket, err := client.Buckets.Get(bucketName).Do()
	if err != nil {
		prometheus.RecordError("storage_api_error", "get_bucket", "service", "storage")
		return nil, err
	}
	return bucket, nil
}

func (client *StorageClient) ListObjectsInBucket(c echo.Context, bucketName string) (*storage.Objects, error) {
	start := time.Now()
	defer func() {
		prometheus.RecordOperation("storage_list_objects", "success", time.Since(start), "service", "storage")
	}()

	objects, err := client.Objects.List(bucketName).Do()
	if err != nil {
		prometheus.RecordError("storage_api_error", "list_objects", "service", "storage")
		return nil, err
	}

	prometheus.RecordCounter("storage_objects_retrieved", int64(len(objects.Items)), "service", "storage")
	return objects, nil
}

// Takes bucket name and returns JSON list of all objects in this bucket.
func (client *StorageClient) ListObjectsInBucketJSON(c echo.Context, bucketName string) (*storage.Objects, error) {
	start := time.Now()
	defer func() {
		prometheus.RecordOperation("storage_list_objects_json", "success", time.Since(start), "service", "storage")
	}()

	objects, err := client.Objects.List(bucketName).Do()
	if err != nil {
		prometheus.RecordError("storage_api_error", "list_objects_json", "service", "storage")
		return nil, err
	}

	prometheus.RecordCounter("storage_objects_json_retrieved", int64(len(objects.Items)), "service", "storage")
	return objects, nil
}

type StorageObject struct {
	Name string
	Data []byte
}

// Takes Bucket name and Object name, returns object struct (objectName and buffered data in []byte format)
func (client *StorageClient) GetObject(c echo.Context, bucketName, objectName string) (*StorageObject, error) {
	start := time.Now()
	defer func() {
		prometheus.RecordOperation("storage_get_object", "success", time.Since(start), "service", "storage")
	}()

	obj, err := client.Objects.Get(bucketName, objectName).Do()
	if err != nil {
		prometheus.RecordError("storage_api_error", "get_object_metadata", "service", "storage")
		return nil, err
	}

	data, err := client.Objects.Get(bucketName, objectName).Download()
	if err != nil {
		prometheus.RecordError("storage_api_error", "download_object", "service", "storage")
		return nil, err
	}
	defer data.Body.Close()
	body, err := io.ReadAll(data.Body)
	if err != nil {
		prometheus.RecordError("storage_io_error", "read_object", "service", "storage")
		return nil, err
	}

	prometheus.RecordHistogram("storage_object_size_bytes", float64(len(body)), "service", "storage")
	return &StorageObject{
		Name: obj.Name,
		Data: body,
	}, nil
}

// Takes bucket Name Object struct (objectName and buffered data in []byte format) and uploads it into Google Cloud Storage specified bucket.
func (client *StorageClient) UploadObject(c echo.Context, bucketName string, obj *StorageObject) error {
	start := time.Now()
	defer func() {
		prometheus.RecordOperation("storage_upload_object", "success", time.Since(start), "service", "storage")
	}()

	reader := bytes.NewReader(obj.Data)

	_, err := client.Objects.Insert(bucketName, &storage.Object{
		Name: obj.Name,
	}).Media(reader).Do()

	if err != nil {
		prometheus.RecordError("storage_api_error", "upload_object", "service", "storage")
		return err
	}

	prometheus.RecordHistogram("storage_upload_size_bytes", float64(len(obj.Data)), "service", "storage")
	return nil
}
