package utils

import (
	"archive/zip"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"google.golang.org/api/gmail/v1"
)

type lockedArray struct {
	sync.Mutex
	ar []string
}

func NewLockedArray() *lockedArray {
	return &lockedArray{ar: make([]string, 0)}
}

func (la *lockedArray) Add(s string) {
	la.Lock()
	la.ar = append(la.ar, s)
	la.Unlock()
}

func (la *lockedArray) Get() []string {
	la.Lock()
	defer la.Unlock()
	return la.ar
}

func init() {
	rand.Seed(time.Now().UnixNano())
}

var letterRunes = []rune("1234567890abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ")

// Creates random string for cookie purposes.
func RandStringRunes(n int) string {
	b := make([]rune, n)
	for i := range b {
		b[i] = letterRunes[rand.Intn(len(letterRunes))]
	}
	return string(b)
}

func Contains(ar []string, b string) bool {
	for _, a := range ar {
		if a == b {
			return true
		}
	}

	return false
}

// DownloadFile will download a url to a local file. It's efficient because it will
// write as it downloads and not load the whole file into memory.
func DownloadFile(filepath string, url string) error {

	// Get the data
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// Create the file
	out, err := os.Create(filepath)
	if err != nil {
		return err
	}
	defer out.Close()

	// Write the body to file
	_, err = io.Copy(out, resp.Body)
	return err
}

func GetStringInBetweenTwoString(str string, startS string, endS string) (result string, found bool) {
	s := strings.Index(str, startS)
	if s == -1 {
		return result, false
	}
	newS := str[s+len(startS):]
	e := strings.Index(newS, endS)
	if e == -1 {
		return result, false
	}
	result = newS[:e]
	return result, true
}

// creates temporary folder for user's cache to avoid situation of conflicts in case different users have the same file name.
func CreateUserTempCacheFolder() string {
	const letterBytes = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"

	rand.Seed(time.Now().UnixNano())

	b := make([]byte, 20)
	for i := range b {
		b[i] = letterBytes[rand.Intn(len(letterBytes))]
	}

	return string(b)
}

// GetEnvWithKey : get env value
func GetEnvWithKey(key string) string {
	return os.Getenv(key)
}

func GenerateTitleFromGmailMessage(msg *gmail.Message) string {
	var from, subject string

	for _, v := range msg.Payload.Headers {
		switch v.Name {
		case "From":
			res, ok := GetStringInBetweenTwoString(v.Value, "\u003c", "\u003e")
			if ok {
				from = res
			} else {
				from = v.Value
			}
		case "Subject":
			subject = v.Value
		}
	}
	return fmt.Sprintf("%s - %s - %s.gmail", from, subject, msg.Id)
}

func Unzip(src, dest string) error {
	r, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer r.Close()

	for _, f := range r.File {
		rc, err := f.Open()
		if err != nil {
			return err
		}
		defer rc.Close()

		fpath := filepath.Join(dest, f.Name)
		if f.FileInfo().IsDir() {
			os.MkdirAll(fpath, f.Mode())
		} else {
			var fdir string
			if lastIndex := strings.LastIndex(fpath, string(os.PathSeparator)); lastIndex > -1 {
				fdir = fpath[:lastIndex]
			}

			err = os.MkdirAll(fdir, f.Mode())
			if err != nil {
				log.Fatal(err)
				return err
			}
			f, err := os.OpenFile(
				fpath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode())
			if err != nil {
				return err
			}
			defer f.Close()

			_, err = io.Copy(f, rc)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func CreateFile(filePath string) (*os.File, error) {
	// Create the directory if it doesn't exist
	dir := filepath.Dir(filePath)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return nil, fmt.Errorf("failed to create directory: %v", err)
		}
	}

	// Create the file
	file, err := os.Create(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to create file: %v", err)
	}
	return file, nil
}

func MaskString(s string) string {
	if len(s) < 4 {
		return s
	}
	return "****************************************************" + s[len(s)-4:]
}
