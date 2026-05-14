package storage

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"be-miawai/internal/models"
)

type CloudStorage interface {
	SaveMessages(conversationID string, messages []models.ConversationMessage) error
	GetMessages(conversationID string) ([]models.ConversationMessage, error)
	DeleteMessages(conversationID string) error
}

type LocalCloudStorage struct {
	BaseDir string
}

func NewLocalCloudStorage(baseDir string) *LocalCloudStorage {
	if strings.TrimSpace(baseDir) == "" {
		baseDir = filepath.Join("storage", "chats")
	}
	return &LocalCloudStorage{BaseDir: baseDir}
}

func (s *LocalCloudStorage) SaveMessages(conversationID string, messages []models.ConversationMessage) error {
	filePath, err := s.filePath(conversationID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.BaseDir, 0755); err != nil {
		return err
	}
	if messages == nil {
		messages = []models.ConversationMessage{}
	}

	data, err := json.MarshalIndent(messages, "", "  ")
	if err != nil {
		return err
	}

	tmpPath := filePath + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmpPath, filePath)
}

func (s *LocalCloudStorage) GetMessages(conversationID string) ([]models.ConversationMessage, error) {
	filePath, err := s.filePath(conversationID)
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return []models.ConversationMessage{}, nil
		}
		return nil, err
	}

	var messages []models.ConversationMessage
	if err := json.Unmarshal(data, &messages); err != nil {
		return nil, err
	}
	if messages == nil {
		return []models.ConversationMessage{}, nil
	}
	return messages, nil
}

func (s *LocalCloudStorage) DeleteMessages(conversationID string) error {
	filePath, err := s.filePath(conversationID)
	if err != nil {
		return err
	}
	if err := os.Remove(filePath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (s *LocalCloudStorage) filePath(conversationID string) (string, error) {
	if strings.TrimSpace(conversationID) == "" {
		return "", errors.New("conversation ID is required")
	}
	if filepath.Base(conversationID) != conversationID {
		return "", errors.New("conversation ID must not contain path separators")
	}
	return filepath.Join(s.BaseDir, conversationID+".json"), nil
}
