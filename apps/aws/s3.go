package aws

import (
	"io"
	"os"
	"time"

	"github.com/StorX2-0/Backup-Tools/pkg/prometheus"
	"github.com/StorX2-0/Backup-Tools/pkg/utils"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/aws/aws-sdk-go/service/s3/s3manager"
)

type AWSsession struct {
	session.Session
}

var AccessKeyID string
var SecretAccessKey string
var MyRegion string

func ConnectAws() *AWSsession {
	start := time.Now()

	AccessKeyID = utils.GetEnvWithKey("AWS_ACCESS_KEY_ID")
	SecretAccessKey = utils.GetEnvWithKey("AWS_SECRET_ACCESS_KEY")
	MyRegion = utils.GetEnvWithKey("AWS_REGION")

	sess, err := session.NewSession(
		&aws.Config{
			Region: aws.String(MyRegion),
			Credentials: credentials.NewStaticCredentials(
				AccessKeyID,
				SecretAccessKey,
				"", // a token will be created when the session it's used.
			),
		})
	if err != nil {
		prometheus.RecordError("aws_connection_failed", "s3")
		panic(err)
	}

	duration := time.Since(start)
	prometheus.RecordTimer("aws_connection_duration", duration, "service", "s3")
	prometheus.RecordCounter("aws_connection_total", 1, "service", "s3", "status", "success")

	return &AWSsession{*sess}
}

type S3FileData struct {
	Name string
	Size int64
}

func (sess *AWSsession) ListFiles(bucket string) ([]S3FileData, error) {
	start := time.Now()

	client := s3.New(sess)
	out, err := client.ListObjectsV2(&s3.ListObjectsV2Input{
		Bucket: &bucket,
	})
	if err != nil {
		prometheus.RecordError("s3_list_failed", "s3")
		return nil, err
	}

	var resp []S3FileData
	for _, obj := range out.Contents {
		resp = append(resp, S3FileData{
			Name: *obj.Key,
			Size: *obj.Size,
		})
	}

	duration := time.Since(start)
	prometheus.RecordTimer("s3_list_duration", duration, "bucket", bucket)
	prometheus.RecordCounter("s3_list_total", 1, "bucket", bucket, "status", "success")
	prometheus.RecordCounter("s3_files_listed_total", int64(len(resp)), "bucket", bucket)

	return resp, nil
}

func (sess *AWSsession) UploadFile(bucket, filename string, data io.Reader) error {
	start := time.Now()

	uploader := s3manager.NewUploader(sess)

	//upload to the s3 bucket
	_, err := uploader.Upload(&s3manager.UploadInput{
		Bucket: aws.String(bucket),
		ACL:    aws.String("public-read"),
		Key:    aws.String(filename),
		Body:   data,
	})
	if err != nil {
		prometheus.RecordError("s3_upload_failed", "s3")
		return err
	}

	duration := time.Since(start)
	prometheus.RecordTimer("s3_upload_duration", duration, "bucket", bucket)
	prometheus.RecordCounter("s3_upload_total", 1, "bucket", bucket, "status", "success")
	prometheus.RecordCounter("s3_files_uploaded_total", 1, "bucket", bucket)

	return nil
}

func (sess *AWSsession) DownloadFile(bucket, name string, file *os.File) error {
	start := time.Now()

	downloader := s3manager.NewDownloader(sess)

	_, err := downloader.Download(file, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(name),
	})
	if err != nil {
		prometheus.RecordError("s3_download_failed", "s3")
		return err
	}

	duration := time.Since(start)
	prometheus.RecordTimer("s3_download_duration", duration, "bucket", bucket)
	prometheus.RecordCounter("s3_download_total", 1, "bucket", bucket, "status", "success")
	prometheus.RecordCounter("s3_files_downloaded_total", 1, "bucket", bucket)

	return nil
}
