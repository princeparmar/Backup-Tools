package dropbox

import (
	"io"
	"os"

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
	cfg := dropbox.Config{
		Token: os.Getenv("DROPBOX_TOKEN"),
	}
	dbx := files.New(cfg)

	return &DropboxClient{dbx}, nil
}

func (dbx *DropboxClient) ListFilesOrFolders(path string) (*files.ListFolderResult, error) {
	res, err := dbx.Client.ListFolder(&files.ListFolderArg{
		Path: path,
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}

func (dbx *DropboxClient) DownloadFile(path string) (*File, error) {
	res, cont, err := dbx.Client.Download(&files.DownloadArg{
		Path: path,
	})
	if err != nil {
		return nil, err
	}
	return &File{
		Name: res.Name,
		Data: cont,
	}, nil

}

func (dbx *DropboxClient) UploadFile(data io.Reader, path string) error {
	_, err := dbx.Client.Upload(&files.UploadArg{
		CommitInfo: files.CommitInfo{
			Path: path,
			Mode: &files.WriteMode{Tagged: dropbox.Tagged{Tag: "add"}},
		},
	}, data)
	if err != nil {
		return err
	}
	return nil
}
