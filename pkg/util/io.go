package util

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

func Unzip(src string, dest string) ([]*os.File, error) {
	r, err := zip.OpenReader(src)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := r.Close(); err != nil {
			panic(err)
		}
	}()

	os.MkdirAll(dest, 0755)

	// Closure to address file descriptors issue with all the deferred .Close() methods
	extractAndWriteFile := func(f *zip.File) (*os.File, error) {
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}

		path := filepath.Join(dest, f.Name)

		// Check for ZipSlip (Directory traversal)
		if !strings.HasPrefix(path, filepath.Clean(dest)+string(os.PathSeparator)) {
			return nil, fmt.Errorf("illegal file path: %s", path)
		}

		if f.FileInfo().IsDir() {
			os.MkdirAll(path, f.Mode())
		} else {
			os.MkdirAll(filepath.Dir(path), f.Mode())
			f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_SYNC, f.Mode())
			if err != nil {
				return nil, err
			}

			if _, err = io.Copy(f, rc); err != nil {
				return nil, err
			}
			if _, err = f.Seek(0, 0); err != nil {
				return nil, err
			}
			return f, nil
		}
		return nil, nil
	}

	resultList := []*os.File{}
	for _, f := range r.File {
		file, err := extractAndWriteFile(f)
		if err != nil {
			continue
		}
		resultList = append(resultList, file)
	}
	return resultList, nil
}

func DeleteFile(filePath string) error {
	if _, err := os.Stat(filePath); err != nil {
		return err
	}

	if err := os.Remove(filePath); err != nil {
		return err
	}

	return nil
}

func MakeDirectoryAndFile(fileName string) (string, error) {
	tn := "temp"
	if err := os.MkdirAll(tn, 0755); err != nil {
		return "", err
	}
	fp := filepath.Join(tn, fileName)
	return fp, nil
}

func WriteFile(reader io.Reader, fileName string) error {
	f, err := os.Create(fileName)
	if err != nil {
		return err
	}

	defer f.Close()
	bytes, err := io.ReadAll(reader)
	if err != nil {
		return err
	}

	if err := os.WriteFile(fileName, bytes, 0644); err != nil {
		return err
	}

	return nil
}

func GetFileExtFromMime(mimeType string) string {
	pattern := `\/([^;\s]+)`
	re := regexp.MustCompile(pattern)

	sm := re.FindStringSubmatch(mimeType)
	if len(sm) < 1 {
		return ""
	}

	return sm[1]
}

func DownloadFileToTempDirectory(data io.Reader) (*os.File, error) {
	tempFile, err := os.CreateTemp("", "discordfile-")
	if err != nil {
		return nil, err
	}
	defer tempFile.Close()
	if _, err = io.Copy(tempFile, data); err != nil {
		return nil, err
	}

	if _, err = tempFile.Seek(0, 0); err != nil {
		return nil, err
	}

	return tempFile, nil
}

func GetDirectoryFromFileName(fileName string) string {
	splitPath := strings.Split(fileName, "/")

	return strings.Join(splitPath[:len(splitPath)-1], "/")
}
