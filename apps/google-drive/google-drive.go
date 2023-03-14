package googledrive

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"storj-integrations/storage"

	"log"
	"net/http"
	"os"

	"github.com/labstack/echo/v4"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/drive/v3"
	"google.golang.org/api/option"
)

func GetFileNames(c echo.Context) error {
	client, err := client(c)
	if err != nil {
		return err
	}

	srv, err := drive.NewService(context.Background(), option.WithHTTPClient(client))
	if err != nil {
		log.Fatalf("Unable to retrieve Drive client: %v", err)
	}

	r, err := srv.Files.List().Fields("nextPageToken, files(id, name)").Do()
	if err != nil {
		log.Fatalf("Unable to retrieve files: %v", err)
	}
	var resp = "Files:\n"
	if len(r.Files) == 0 {
		resp = resp + "No files found."
	} else {
		for _, i := range r.Files {
			resp = fmt.Sprintf("%s\n%s (%s)", resp, i.Name, i.Id)
		}
	}
	return c.String(200, resp)
}

func GetFileByID(c echo.Context) error {
	id := c.Param("ID")

	name, data, err := GetFile(c, id)
	if err != nil {
		return c.String(http.StatusForbidden, "error")
	}

	return c.String(200, "name:\n"+name+"\nData: "+string(data))

}

func client(c echo.Context) (*http.Client, error) {
	database := c.Get(dbContextKey).(*storage.PosgresStore)

	b, err := os.ReadFile("credentials.json")
	if err != nil {
		log.Fatalf("Unable to read client secret file: %v", err)
	}
	config, err := google.ConfigFromJSON(b, drive.DriveScope)
	if err != nil {
		log.Fatalf("Unable to parse client secret file to config: %v", err)
	}

	tok, err := database.ReadGoogleAuthToken("cookie1234")
	if err != nil {
		return nil, c.String(http.StatusUnauthorized, "user is not authorized")
	}
	client := config.Client(context.Background(), tok)

	return client, nil
}

func GetFile(c echo.Context, id string) (string, []byte, error) {
	client, err := client(c)
	if err != nil {
		return "", nil, err
	}

	srv, err := drive.NewService(context.Background(), option.WithHTTPClient(client))
	if err != nil {
		log.Fatalf("Unable to retrieve Drive client: %v", err)
	}

	file, _ := srv.Files.Get(id).Do()
	res, err := srv.Files.Get(id).Download()
	if err != nil {
		log.Fatalf("Error: %v", err)
	}
	data, _ := io.ReadAll(res.Body)

	return file.Name, data, nil
}

func UploadFile(c echo.Context, name string, data []byte) error {
	client, err := client(c)
	if err != nil {
		return err
	}
	srv, err := drive.NewService(context.Background(), option.WithHTTPClient(client))
	if err != nil {
		return err
	}
	_, err = srv.Files.Create(
		&drive.File{
			Name: name,
		}).Media(
		bytes.NewReader(data),
	).Do()
	if err != nil {
		return err
	}
	return nil
}
