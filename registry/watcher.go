package registry

import "time"

const (
	// Create is emitted when a new service is registered
	Create EventType = iota
	// Delete is emitted when an existing service is deregsitered
	Delete
	// Update is emitted when an existing servicec is updated
	Update
)

type (
	// Watcher is an interface that returns updates
	// about services within the registry.
	Watcher interface {
		// Next is a blocking call
		Next() (*Result, error)
		Stop()
	}

	// Result is returned by a call to Next on
	// the watcher. Actions can be create, update, delete
	Result struct {
		Action  string
		Service *Service
	}

	// EventType defines registry event type
	EventType int

	// Event is registry event
	Event struct {
		// Id is registry id
		Id string
		// Type defines type of event
		Type EventType
		// Timestamp is event timestamp
		Timestamp time.Time
		// Service is registry service
		Service *Service
	}
)

// String returns human readable event type
func (t EventType) String() string {
	switch t {
	case Create:
		return "create"
	case Delete:
		return "delete"
	case Update:
		return "update"
	default:
		return "unknown"
	}
}
