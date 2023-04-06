package storj

import (
	"context"
	"log"
	"testing"
)

func TestUploadObject(t *testing.T) {
	err := UploadObject(context.Background(), "myAccessGrant", "myBucket", "test.txt", []byte("hello, it's test!"))
	if err != nil {
		log.Fatalln("error:", err)
	}
}
