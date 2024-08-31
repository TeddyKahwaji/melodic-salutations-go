package gcp

import (
	"encoding/json"
	"os"
	"strings"
)

type GcpCredentials struct {
	ClientEmail  string `json:"client_email" structs:"client_email" mapstructure:"client_email"`
	ClientId     string `json:"client_id" structs:"client_id" mapstructure:"client_id"`
	PrivateKeyId string `json:"private_key_id" structs:"private_key_id" mapstructure:"private_key_id"`
	PrivateKey   string `json:"private_key" structs:"private_key" mapstructure:"private_key"`
	ProjectId    string `json:"project_id" structs:"project_id" mapstructure:"project_id"`
	Type         string `json:"type" structs:"type" mapstructure:"type"`
}

func GetCredentials() ([]byte, error) {
	projectId := os.Getenv("GCP_PROJECT_ID")
	privateKey := strings.Join(strings.Split(os.Getenv("GCP_PRIVATE_KEY"), "\\n"), "\n")

	credentials := &GcpCredentials{
		ClientEmail:  os.Getenv("GCP_CLIENT_EMAIL"),
		ClientId:     os.Getenv("GCP_CLIENT_ID"),
		PrivateKeyId: os.Getenv("GCP_PRIVATE_KEY_ID"),
		PrivateKey:   privateKey,
		ProjectId:    projectId,
		Type:         "service_account",
	}

	data, err := json.Marshal(credentials)
	if err != nil {
		return nil, err
	}

	return data, nil
}
