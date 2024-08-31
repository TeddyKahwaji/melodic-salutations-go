package firebasehelper

import (
	"context"
	"errors"
	"io"
	"os"
	"time"

	"cloud.google.com/go/firestore"
	"cloud.google.com/go/storage"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type Firebase interface {
	GetDocumentFromCollection(ctx context.Context, collection string, document string) (map[string]interface{}, error)
	GenerateSignedUrl(bucketName string, objectName string) (string, error)
	DownloadFileBytes(ctx context.Context, bucketName string, objectName string) (io.Reader, error)
	UploadFileToStorage(ctx context.Context, bucketName string, objectName string, file *os.File, fileName string) error
	UpdateDocument(ctx context.Context, collection string, document string, data interface{}) error
}

type firebaseAdapter struct {
	firestoreClient    *firestore.Client
	cloudStorageClient *storage.Client
}

func NewFirebaseHelper(firestoreClient *firestore.Client, storageClient *storage.Client) Firebase {
	return &firebaseAdapter{
		firestoreClient:    firestoreClient,
		cloudStorageClient: storageClient,
	}
}

func (f *firebaseAdapter) UploadFileToStorage(ctx context.Context, bucketName string, objectName string, file *os.File, fileName string) error {
	defer file.Close()
	bucket := f.cloudStorageClient.Bucket(bucketName)
	wc := bucket.Object(objectName).NewWriter(ctx)
	token, _ := uuid.NewV7()
	metadata := map[string]string{"firebaseStorageDownloadTokens": token.String()}
	wc.Metadata = metadata
	wc.ContentType = "audio/mpeg"
	if _, err := io.Copy(wc, file); err != nil {
		return err
	}

	return wc.Close()
}

func (f *firebaseAdapter) GetDocumentFromCollection(ctx context.Context, collection string, document string) (map[string]interface{}, error) {
	fs, err := f.firestoreClient.Collection(collection).Doc(document).Get(ctx)
	if err != nil {
		return nil, err
	}
	data := fs.Data()

	return data, nil
}

func (f *firebaseAdapter) UpdateDocument(ctx context.Context, collection string, document string, data interface{}) error {
	if _, err := f.GetDocumentFromCollection(ctx, collection, document); err != nil {
		if status.Code(err) == codes.NotFound {
			if _, err := f.firestoreClient.Collection(collection).Doc(document).Set(ctx, data); err != nil {
				return err
			}
		}
		return err
	}

	updates := []firestore.Update{}
	if castedData, ok := data.(map[string]interface{}); ok {
		for key, value := range castedData {
			updates = append(updates, firestore.Update{
				Path:  key,
				Value: value,
			})
		}
		if _, err := f.firestoreClient.Collection(collection).Doc(document).Update(ctx, updates); err != nil {
			return err
		}
		return nil
	}

	return errors.New("unexpected data value")
}

func (f *firebaseAdapter) GenerateSignedUrl(bucketName string, objectName string) (string, error) {
	bucket := f.cloudStorageClient.Bucket(bucketName)
	object, err := bucket.SignedURL(objectName, &storage.SignedURLOptions{
		Scheme:  storage.SigningSchemeV4,
		Method:  "GET",
		Expires: time.Now().Add(15 * time.Minute),
	})
	if err != nil {
		return "", err
	}

	return object, nil
}

func (f *firebaseAdapter) DownloadFileBytes(ctx context.Context, bucketName string, objectName string) (io.Reader, error) {
	bucket := f.cloudStorageClient.Bucket(bucketName)
	object := bucket.Object(objectName)
	reader, err := object.NewReader(ctx)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	return reader, nil
}
