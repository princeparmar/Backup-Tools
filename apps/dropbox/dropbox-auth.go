package dropbox

import (
	"io"
	"time"

	"github.com/StorX2-0/Backup-Tools/pkg/prometheus"
	"github.com/StorX2-0/Backup-Tools/pkg/utils"
	"github.com/dropbox/dropbox-sdk-go-unofficial/v6/dropbox"
	"github.com/dropbox/dropbox-sdk-go-unofficial/v6/dropbox/files"
)

type DropboxClient struct {
	files.Client
}

type File struct {
	Name string
	Data io.ReadCloser
}

func NewDropboxClient() (*DropboxClient, error) {
	start := time.Now()

	cfg := dropbox.Config{
		Token: utils.GetEnvWithKey("DROPBOX_TOKEN"),
	}
	dbx := files.New(cfg)

	duration := time.Since(start)
	prometheus.RecordTimer("dropbox_client_creation_duration", duration, "service", "dropbox")
	prometheus.RecordCounter("dropbox_client_creation_total", 1, "service", "dropbox", "status", "success")

	return &DropboxClient{dbx}, nil
}

func (dbx *DropboxClient) ListFilesOrFolders(path string) (*files.ListFolderResult, error) {
	start := time.Now()

	res, err := dbx.Client.ListFolder(&files.ListFolderArg{
		Path: path,
	})
	if err != nil {
		prometheus.RecordError("dropbox_list_failed", "dropbox")
		return nil, err
	}

	duration := time.Since(start)
	prometheus.RecordTimer("dropbox_list_duration", duration, "path", path)
	prometheus.RecordCounter("dropbox_list_total", 1, "path", path, "status", "success")
	prometheus.RecordCounter("dropbox_items_listed_total", int64(len(res.Entries)), "path", path)

	return res, nil
}

func (dbx *DropboxClient) DownloadFile(path string) (*File, error) {
	start := time.Now()

	res, cont, err := dbx.Client.Download(&files.DownloadArg{
		Path: path,
	})
	if err != nil {
		prometheus.RecordError("dropbox_download_failed", "dropbox")
		return nil, err
	}

	duration := time.Since(start)
	prometheus.RecordTimer("dropbox_download_duration", duration, "path", path)
	prometheus.RecordCounter("dropbox_download_total", 1, "path", path, "status", "success")
	prometheus.RecordCounter("dropbox_files_downloaded_total", 1, "path", path)

	return &File{
		Name: res.Name,
		Data: cont,
	}, nil
}

func (dbx *DropboxClient) UploadFile(data io.Reader, path string) error {
	start := time.Now()

	_, err := dbx.Client.Upload(&files.UploadArg{
		CommitInfo: files.CommitInfo{
			Path: path,
			Mode: &files.WriteMode{Tagged: dropbox.Tagged{Tag: "add"}},
		},
	}, data)
	if err != nil {
		prometheus.RecordError("dropbox_upload_failed", "dropbox")
		return err
	}

	duration := time.Since(start)
	prometheus.RecordTimer("dropbox_upload_duration", duration, "path", path)
	prometheus.RecordCounter("dropbox_upload_total", 1, "path", path, "status", "success")
	prometheus.RecordCounter("dropbox_files_uploaded_total", 1, "path", path)

	return nil
}
