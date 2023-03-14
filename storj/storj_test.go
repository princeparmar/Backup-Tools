package storj

import (
	"context"
	"fmt"
	"log"
	"testing"
)

func TestUploadAndDownload(t *testing.T) {
	err := UploadAndDownloadData(context.Background(), "myAccessGrant", "myBucket", "myObjectKey", []byte("myData"))
	if err != nil {
		log.Fatalln("error:", err)
	}

	fmt.Println("success!")
}

func TestUploadObject(t *testing.T) {
	err := UploadObject(context.Background(), "myAccessGrant", "myBucket", "test.txt", []byte("hello, it's test!"))
	if err != nil {
		log.Fatalln("error:", err)
	}
}
