package google

import (
	"bytes"
	"context"
	"io"

	"github.com/labstack/echo/v4"
	"google.golang.org/api/option"
	"google.golang.org/api/storage/v1"
)

type StorageClient struct {
	*storage.Service
}

func NewGoogleStorageClient(c echo.Context) (*StorageClient, error) {
	client, err := client(c)
	if err != nil {
		return nil, err
	}

	srv, err := storage.NewService(context.Background(), option.WithHTTPClient(client))
	if err != nil {
		return nil, err
	}
	return &StorageClient{srv}, nil
}

// Takes project name and returns JSON list of all buckets in this project.
func (client *StorageClient) ListBucketsJSON(c echo.Context, projectName string) (*storage.Buckets, error) {
	bucketList, err := client.Buckets.List(projectName).Do()
	if err != nil {
		return nil, err
	}
	/*listJSON, err := bucketList.MarshalJSON()
	if err != nil {
		return "", err
	}*/
	return bucketList, nil
}

func (client *StorageClient) GetBucket(c echo.Context, bucketName string) (*storage.Bucket, error) {
	bucket, err := client.Buckets.Get(bucketName).Do()
	if err != nil {
		return nil, err
	}
	return bucket, nil
}

func (client *StorageClient) ListObjectsInBucket(c echo.Context, bucketName string) (*storage.Objects, error) {
	objects, err := client.Objects.List(bucketName).Do()
	if err != nil {
		return nil, err
	}

	return objects, nil
}

// Takes bucket name and returns JSON list of all objects in this bucket.
func (client *StorageClient) ListObjectsInBucketJSON(c echo.Context, bucketName string) (*storage.Objects, error) {
	return client.Objects.List(bucketName).Do()

}

type StorageObject struct {
	Name string
	Data []byte
}

// Takes Bucket name and Object name, returns object struct (objectName and buffered data in []byte format)
func (client *StorageClient) GetObject(c echo.Context, bucketName, objectName string) (*StorageObject, error) {
	obj, err := client.Objects.Get(bucketName, objectName).Do()
	if err != nil {
		return nil, err
	}

	data, err := client.Objects.Get(bucketName, objectName).Download()
	if err != nil {
		return nil, err
	}
	defer data.Body.Close()
	body, err := io.ReadAll(data.Body)
	if err != nil {
		return nil, err
	}
	return &StorageObject{
		Name: obj.Name,
		Data: body,
	}, nil
}

// Takes bucket Name Object struct (objectName and buffered data in []byte format) and uploads it into Google Cloud Storage specified bucket.
func (client *StorageClient) UploadObject(c echo.Context, bucketName string, obj *StorageObject) error {
	reader := bytes.NewReader(obj.Data)

	_, err := client.Objects.Insert(bucketName, &storage.Object{
		Name: obj.Name,
	}).Media(reader).Do()

	if err != nil {
		return err
	}

	return nil
}
