package aws

import (
	"io"
	"os"

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
		panic(err)
	}

	return &AWSsession{*sess}
}

type S3FileData struct {
	Name string
	Size int64
}

func (sess *AWSsession) ListFiles(bucket string) ([]S3FileData, error) {
	client := s3.New(sess)
	out, err := client.ListObjectsV2(&s3.ListObjectsV2Input{
		Bucket: &bucket,
	})
	if err != nil {
		return nil, err
	}

	var resp []S3FileData
	for _, obj := range out.Contents {
		resp = append(resp, S3FileData{
			Name: *obj.Key,
			Size: *obj.Size,
		})
	}

	return resp, nil
}

func (sess *AWSsession) UploadFile(bucket, filename string, data io.Reader) error {

	uploader := s3manager.NewUploader(sess)

	//upload to the s3 bucket
	_, err := uploader.Upload(&s3manager.UploadInput{
		Bucket: aws.String(bucket),
		ACL:    aws.String("public-read"),
		Key:    aws.String(filename),
		Body:   data,
	})
	if err != nil {
		return err
	}

	return nil
}

func (sess *AWSsession) DownloadFile(bucket, name string, file *os.File) error {
	downloader := s3manager.NewDownloader(sess)

	_, err := downloader.Download(file, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(name),
	})
	if err != nil {
		return err
	}
	return nil
}
