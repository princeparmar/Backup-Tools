package utils

import (
	"archive/zip"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/StorX2-0/Backup-Tools/apps/outlook"
	"google.golang.org/api/gmail/v1"
)

type LockedArray struct {
	sync.Mutex
	items []string
}

func NewLockedArray() *LockedArray {
	return &LockedArray{items: make([]string, 0)}
}

func (la *LockedArray) Add(s string) {
	la.Lock()
	defer la.Unlock()
	la.items = append(la.items, s)
}

func (la *LockedArray) Get() []string {
	la.Lock()
	defer la.Unlock()
	return la.items
}

const letters = "1234567890abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"

func RandStringRunes(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = letters[rand.Intn(len(letters))]
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

func DownloadFile(filepath, url string) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	out, err := os.Create(filepath)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, resp.Body)
	return err
}

func GetStringBetween(str, start, end string) (string, bool) {
	s := strings.Index(str, start)
	if s == -1 {
		return "", false
	}

	newStr := str[s+len(start):]
	e := strings.Index(newStr, end)
	if e == -1 {
		return "", false
	}

	return newStr[:e], true
}

func CreateUserTempCacheFolder() string {
	return RandStringRunes(20)
}

func GetEnvWithKey(key string) string {
	return os.Getenv(key)
}

func GenerateTitleFromGmailMessage(msg *gmail.Message) string {
	var from, subject string

	for _, v := range msg.Payload.Headers {
		switch v.Name {
		case "From":
			if res, ok := GetStringBetween(v.Value, "\u003c", "\u003e"); ok {
				from = res
			} else {
				from = v.Value
			}
		case "Subject":
			subject = v.Value
		}
	}

	title := fmt.Sprintf("%s - %s - %s.gmail", from, subject, msg.Id)
	return strings.ReplaceAll(title, "/", "_")
}

func GenerateTitleFromOutlookMessage(msg *outlook.OutlookMinimalMessage) string {
	return fmt.Sprintf("%s - %s - %s.outlook", msg.From, msg.Subject, msg.ID)
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

		fpath := filepath.Join(dest, f.Name)
		if f.FileInfo().IsDir() {
			os.MkdirAll(fpath, f.Mode())
			rc.Close()
			continue
		}

		if err := os.MkdirAll(filepath.Dir(fpath), 0755); err != nil {
			rc.Close()
			return err
		}

		out, err := os.OpenFile(fpath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode())
		if err != nil {
			rc.Close()
			return err
		}

		if _, err := io.Copy(out, rc); err != nil {
			out.Close()
			rc.Close()
			return err
		}

		out.Close()
		rc.Close()
	}
	return nil
}

func CreateFile(filePath string) (*os.File, error) {
	dir := filepath.Dir(filePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create directory: %v", err)
	}

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
	return strings.Repeat("*", len(s)-4) + s[len(s)-4:]
}
