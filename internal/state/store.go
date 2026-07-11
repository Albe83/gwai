// Package state defines the narrow persistence contract used by the control
// plane. Dapr is an infrastructure adapter; domain code does not import a Dapr
// SDK or depend on a specific database.
package state

import (
	"context"
	"encoding/json"
	"errors"
)

var ErrConflict = errors.New("state concurrency conflict")

type Entry struct {
	Value  json.RawMessage
	ETag   string
	Exists bool
}

type OperationType string

const (
	Upsert OperationType = "upsert"
	Delete OperationType = "delete"
)

type Operation struct {
	Type  OperationType
	Key   string
	Value json.RawMessage
	ETag  string
}

type Store interface {
	Get(context.Context, string) (Entry, error)
	Transact(context.Context, []Operation) error
}
