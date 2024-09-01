package util

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

func Unzip(src string, dest string) ([]*os.File, error) {
	zipReader, err := zip.OpenReader(src)
	if err != nil {
		return nil, fmt.Errorf("error opening zip reader %w", err)
	}

	defer func() {
		if err := zipReader.Close(); err != nil {
			panic(err)
		}
	}()

	if err := os.MkdirAll(dest, 0o755); err != nil {
		return nil, err
	}

	// Closure to address file descriptors issue with all the deferred .Close() methods
	extractAndWriteFile := func(file *zip.File) (*os.File, error) {
		rc, err := file.Open()
		if err != nil {
			return nil, err
		}

		path := filepath.Join(dest, file.Name)

		// Check for ZipSlip (Directory traversal)
		if !strings.HasPrefix(path, filepath.Clean(dest)+string(os.PathSeparator)) {
			return nil, errors.New("illegal file path")
		}

		if file.FileInfo().IsDir() {
			if err := os.MkdirAll(path, file.Mode()); err != nil {
				return nil, err
			}
		} else {
			if err := os.MkdirAll(filepath.Dir(path), file.Mode()); err != nil {
				return nil, err
			}

			file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_SYNC, file.Mode())
			if err != nil {
				return nil, err
			}

			if _, err = io.Copy(file, rc); err != nil {
				return nil, err
			}

			if _, err = file.Seek(0, 0); err != nil {
				return nil, err
			}

			return file, nil
		}

		return nil, errors.New("unexpected error occurred")
	}

	resultList := []*os.File{}

	for _, f := range zipReader.File {
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
	if err := os.MkdirAll(tn, 0o755); err != nil {
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

	if err := os.WriteFile(fileName, bytes, 0o644); err != nil {
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
