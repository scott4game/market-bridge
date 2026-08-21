package market

import "time"

const SchemaVersion = "1"

type Partition struct {
	Name      string    `json:"name"`
	Symbol    string    `json:"symbol"`
	Year      int       `json:"year"`
	Rows      int       `json:"rows"`
	From      time.Time `json:"from"`
	To        time.Time `json:"to"`
	SHA256    string    `json:"sha256"`
	SizeBytes int64     `json:"size_bytes"`
}

type Manifest struct {
	DatasetID     string      `json:"dataset_id"`
	Spec          DatasetSpec `json:"spec"`
	Provider      string      `json:"provider"`
	SchemaVersion string      `json:"schema_version"`
	DataVersion   string      `json:"data_version"`
	GeneratedAt   time.Time   `json:"generated_at"`
	Partitions    []Partition `json:"partitions"`
}

type DatasetStatus struct {
	DatasetID string `json:"dataset_id"`
	State     string `json:"state"`
	Error     string `json:"error,omitempty"`
}
