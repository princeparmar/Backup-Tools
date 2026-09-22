package satellite

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/aws/aws-sdk-go/service/s3/s3manager"
)

// GatewayS3TokenType marks a storx_token as gateway S3 credentials for any
// S3-compatible external app (not AWS-only). The gateway forwards requests.
const GatewayS3TokenType = "gateway_s3"

// GatewayS3Token is the JSON shape stored in Backup-Tools storx_token for external S3.
type GatewayS3Token struct {
	Type        string `json:"type"`
	AccessKeyID string `json:"access_key_id"`
	SecretKey   string `json:"secret_key"`
	Endpoint    string `json:"endpoint"`
}

// ParseGatewayS3Token returns credentials when token is a gateway_s3 JSON blob.
func ParseGatewayS3Token(token string) (*GatewayS3Token, bool) {
	token = strings.TrimSpace(token)
	if token == "" || token[0] != '{' {
		return nil, false
	}
	var t GatewayS3Token
	if err := json.Unmarshal([]byte(token), &t); err != nil {
		return nil, false
	}
	if t.Type != GatewayS3TokenType {
		return nil, false
	}
	if strings.TrimSpace(t.AccessKeyID) == "" || strings.TrimSpace(t.SecretKey) == "" || strings.TrimSpace(t.Endpoint) == "" {
		return nil, false
	}
	return &t, true
}

// externalS3BucketAlias maps default reserved buckets → cyberls-* names used only on external S3.
var externalS3BucketAlias = map[string]string{
	ReserveBucket_Gmail:       ExternalS3Bucket_Gmail,
	ExternalS3Bucket_Gmail:    ExternalS3Bucket_Gmail,
	ReserveBucket_Drive:       ExternalS3Bucket_Drive,
	ExternalS3Bucket_Drive:    ExternalS3Bucket_Drive,
	ReserveBucket_Contacts:    ExternalS3Bucket_Contacts,
	ExternalS3Bucket_Contacts: ExternalS3Bucket_Contacts,
	ReserveBucket_Calendar:    ExternalS3Bucket_Calendar,
	ExternalS3Bucket_Calendar: ExternalS3Bucket_Calendar,
}

// BucketForAccess returns the bucket name to use for this access token.
// External S3 (gateway_s3) uses cyberls-* vault names; all other flows keep the default names.
func BucketForAccess(accessGrant, bucketName string) string {
	if _, ok := ParseGatewayS3Token(accessGrant); !ok {
		return bucketName
	}
	if mapped, ok := externalS3BucketAlias[bucketName]; ok {
		return mapped
	}
	return bucketName
}

func newGatewayS3Client(tok *GatewayS3Token) (*s3.S3, error) {
	endpoint := strings.TrimRight(strings.TrimSpace(tok.Endpoint), "/")
	cfg := &aws.Config{
		Credentials:      credentials.NewStaticCredentials(tok.AccessKeyID, tok.SecretKey, ""),
		Endpoint:         aws.String(endpoint),
		Region:           aws.String("us-east-1"),
		S3ForcePathStyle: aws.Bool(true),
		DisableSSL:       aws.Bool(strings.HasPrefix(endpoint, "http://")),
		HTTPClient:       satelliteHTTPClient,
	}
	sess, err := session.NewSession(cfg)
	if err != nil {
		return nil, fmt.Errorf("create gateway s3 session: %w", err)
	}
	return s3.New(sess), nil
}

func uploadViaGatewayS3(ctx context.Context, tok *GatewayS3Token, bucketName, objectKey string, r io.Reader, meta map[string]string) error {
	client, err := newGatewayS3Client(tok)
	if err != nil {
		return err
	}

	// Do not auto-create buckets — external apps must already have the bucket.
	input := &s3manager.UploadInput{
		Bucket: aws.String(bucketName),
		Key:    aws.String(objectKey),
		Body:   r,
	}
	if len(meta) > 0 {
		md := make(map[string]*string)
		for k, v := range meta {
			lk := strings.ToLower(strings.TrimSpace(k))
			val := v
			switch lk {
			case "content-type":
				input.ContentType = aws.String(val)
			case "content-encoding":
				input.ContentEncoding = aws.String(val)
			case "content-disposition":
				input.ContentDisposition = aws.String(val)
			case "content-language":
				input.ContentLanguage = aws.String(val)
			case "cache-control":
				input.CacheControl = aws.String(val)
			case "expires":
				// skip — not a user metadata field
			default:
				md[k] = &val
			}
		}
		if len(md) > 0 {
			input.Metadata = md
		}
	}
	if input.ContentType == nil {
		input.ContentType = aws.String("application/octet-stream")
	}

	uploader := s3manager.NewUploaderWithClient(client)
	_, err = uploader.UploadWithContext(ctx, input)
	if err != nil {
		return fmt.Errorf("gateway s3 upload: %w", err)
	}
	return nil
}

func downloadViaGatewayS3(ctx context.Context, tok *GatewayS3Token, bucketName, objectKey string, w io.Writer) error {
	client, err := newGatewayS3Client(tok)
	if err != nil {
		return err
	}
	out, err := client.GetObjectWithContext(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucketName),
		Key:    aws.String(objectKey),
	})
	if err != nil {
		return fmt.Errorf("gateway s3 download: %w", err)
	}
	defer out.Body.Close()
	if _, err = io.Copy(w, out.Body); err != nil {
		return fmt.Errorf("gateway s3 download copy: %w", err)
	}
	return nil
}

func listViaGatewayS3(ctx context.Context, tok *GatewayS3Token, bucketName, prefix string) (map[string]bool, error) {
	client, err := newGatewayS3Client(tok)
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool)
	var token *string
	for {
		resp, err := client.ListObjectsV2WithContext(ctx, &s3.ListObjectsV2Input{
			Bucket:            aws.String(bucketName),
			Prefix:            aws.String(prefix),
			ContinuationToken: token,
		})
		if err != nil {
			// Missing bucket → empty list (same UX as vault browser).
			if isGatewayNoSuchBucket(err) {
				return out, nil
			}
			return nil, fmt.Errorf("gateway s3 list: %w", err)
		}
		for _, obj := range resp.Contents {
			if obj.Key != nil && *obj.Key != "" {
				out[*obj.Key] = true
			}
		}
		if aws.BoolValue(resp.IsTruncated) && resp.NextContinuationToken != nil {
			token = resp.NextContinuationToken
			continue
		}
		break
	}
	return out, nil
}

func headViaGatewayS3(ctx context.Context, tok *GatewayS3Token, bucketName, objectKey string) (map[string]string, int64, error) {
	client, err := newGatewayS3Client(tok)
	if err != nil {
		return nil, 0, err
	}
	out, err := client.HeadObjectWithContext(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(bucketName),
		Key:    aws.String(objectKey),
	})
	if err != nil {
		return nil, 0, fmt.Errorf("gateway s3 head: %w", err)
	}
	meta := make(map[string]string)
	for k, v := range out.Metadata {
		if v != nil {
			meta[k] = *v
		}
	}
	var size int64
	if out.ContentLength != nil {
		size = *out.ContentLength
	}
	return meta, size, nil
}

func isGatewayNoSuchBucket(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "nosuchbucket") ||
		strings.Contains(msg, "status code: 404") ||
		strings.Contains(msg, fmt.Sprintf("%d", http.StatusNotFound))
}