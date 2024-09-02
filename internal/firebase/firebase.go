package firebasehelper

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	fs "cloud.google.com/go/firestore"
	gs "cloud.google.com/go/storage"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

type Firebase interface {
	GetDocumentFromCollection(ctx context.Context, collection string, document string) (map[string]interface{}, error)
	GenerateSignedURL(bucketName string, objectName string) (string, error)
	DownloadFileBytes(ctx context.Context, bucketName string, objectName string) (io.Reader, error)
	UploadFileToStorage(ctx context.Context, bucketName string, objectName string, file *os.File, fileName string) error
	UpdateDocument(ctx context.Context, collection string, document string, data map[string]interface{}) error
	CreateDocument(ctx context.Context, collection string, document string, data interface{}) error
	DeleteDocument(ctx context.Context, collection string, document string) error
}

type firebaseAdapter struct {
	firestoreClient    *fs.Client
	cloudStorageClient *gs.Client
	logger             *zap.Logger
}

func NewFirebaseHelper(firestoreClient *fs.Client, storageClient *gs.Client, logger *zap.Logger) *firebaseAdapter {
	return &firebaseAdapter{
		firestoreClient:    firestoreClient,
		cloudStorageClient: storageClient,
		logger:             logger,
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
		return nil, fmt.Errorf("error getting document from collection %w", err)
	}

	data := fs.Data()

	return data, nil
}

func (f *firebaseAdapter) CreateDocument(ctx context.Context, collection string, document string, data interface{}) error {
	_, err := f.firestoreClient.Collection(collection).Doc(document).Create(ctx, data)

	return err
}

func (f *firebaseAdapter) DeleteDocument(ctx context.Context, collection string, document string) error {
	_, err := f.firestoreClient.Collection(collection).Doc(document).Delete(ctx)

	if err != nil {
		return fmt.Errorf("error deleting document from collection: %w", err)
	}

	return err
}

func (f *firebaseAdapter) UpdateDocument(ctx context.Context, collection string, document string, data map[string]interface{}) error {
	updates := []fs.Update{}

	for key, value := range data {
		updates = append(updates, fs.Update{
			Path:  key,
			Value: value,
		})
	}

	if _, err := f.firestoreClient.Collection(collection).Doc(document).Update(ctx, updates); err != nil {
		return fmt.Errorf("error updating document: %w", err)
	}

	return nil
}

func (f *firebaseAdapter) GenerateSignedURL(bucketName string, objectName string) (string, error) {
	bucket := f.cloudStorageClient.Bucket(bucketName)
	object, err := bucket.SignedURL(objectName, &gs.SignedURLOptions{
		Scheme:  gs.SigningSchemeV4,
		Method:  "GET",
		Expires: time.Now().Add(15 * time.Minute),
	})
	if err != nil {
		return "", fmt.Errorf("error signing url: %w", err)
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

	defer func() {
		if err := reader.Close(); err != nil {
			f.logger.Error("error closing reader when downloading file bytes", zap.Error(err), zap.String("bucket_name", bucketName), zap.String("object_name", objectName))
		}
	}()

	return reader, nil
}
