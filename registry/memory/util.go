package memory

import (
	"context"
	"time"

	"github.com/volts-dev/volts/registry"
)

type servicesKey struct{}

func getServiceRecords(ctx context.Context) map[string]map[string]*record {
	memServices, ok := ctx.Value(servicesKey{}).(map[string][]*registry.Service)
	if !ok {
		return nil
	}

	services := make(map[string]map[string]*record)

	for name, svc := range memServices {
		if _, ok := services[name]; !ok {
			services[name] = make(map[string]*record)
		}
		// go through every version of the service
		for _, s := range svc {
			services[s.Name][s.Version] = serviceToRecord(s, 0)
		}
	}

	return services
}

func serviceToRecord(s *registry.Service, ttl time.Duration) *record {
	metadata := make(map[string]string, len(s.Metadata))
	for k, v := range s.Metadata {
		metadata[k] = v
	}

	nodes := make(map[string]*node, len(s.Nodes))
	for _, n := range s.Nodes {
		nodes[n.Id] = &node{
			Node:     n,
			TTL:      ttl,
			LastSeen: time.Now(),
		}
	}

	endpoints := make([]*registry.Endpoint, len(s.Endpoints))
	for i, e := range s.Endpoints {
		endpoints[i] = e
	}

	return &record{
		Name:      s.Name,
		Version:   s.Version,
		Metadata:  metadata,
		Nodes:     nodes,
		Endpoints: endpoints,
	}
}

func recordToService(r *record) *registry.Service {
	metadata := make(map[string]string, len(r.Metadata))
	for k, v := range r.Metadata {
		metadata[k] = v
	}

	endpoints := make([]*registry.Endpoint, len(r.Endpoints))
	for i, e := range r.Endpoints {
		request := new(registry.Value)
		if e.Request != nil {
			*request = *e.Request
		}
		response := new(registry.Value)
		if e.Response != nil {
			*response = *e.Response
		}

		metadata := make(map[string]string, len(e.Metadata))
		for k, v := range e.Metadata {
			metadata[k] = v
		}

		endpoints[i] = &registry.Endpoint{
			Name:        e.Name,
			Host:        e.Host,
			Method:      e.Method,
			Path:        e.Path,
			Description: e.Description,
			Stream:      e.Stream,
			Handler:     e.Handler,
			Body:        e.Body,
			Request:     request,
			Response:    response,
			Metadata:    metadata,
		}
	}

	nodes := make([]*registry.Node, len(r.Nodes))
	i := 0
	for _, n := range r.Nodes {
		metadata := make(map[string]string, len(n.Metadata))
		for k, v := range n.Metadata {
			metadata[k] = v
		}

		nodes[i] = &registry.Node{
			Id:       n.Id,
			Address:  n.Address,
			Metadata: metadata,
		}
		i++
	}

	return &registry.Service{
		Name:      r.Name,
		Version:   r.Version,
		Metadata:  metadata,
		Endpoints: endpoints,
		Nodes:     nodes,
	}
}
