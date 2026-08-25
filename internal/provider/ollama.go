package provider

import (
	"context"
	"strings"
	"time"

	"github.com/maci0/tokentop/internal/core"
)

// Ollama monitors an Ollama daemon via /api/ps. Ollama publishes no token
// counters over HTTP, so throughput comes from probes or agent events.
type Ollama struct {
	base    string
	label   string
	version versionCache
}

func NewOllama(base string) *Ollama {
	return &Ollama{base: strings.TrimRight(base, "/"), label: "ollama"}
}

func (o *Ollama) Label() string { return o.label }
func (o *Ollama) Addr() string  { return o.base }

func (o *Ollama) Poll(ctx context.Context) (*Metrics, error) {
	m := &Metrics{}
	var ps struct {
		Models []struct {
			Name     string    `json:"name"`
			Model    string    `json:"model"`
			Size     uint64    `json:"size"`
			SizeVRAM uint64    `json:"size_vram"`
			Expires  time.Time `json:"expires_at"`
		} `json:"models"`
	}
	if err := getJSON(ctx, o.base+"/api/ps", &ps); err != nil {
		return nil, err
	}
	for _, mm := range ps.Models {
		name := mm.Name
		if name == "" {
			name = mm.Model
		}
		m.Models = append(m.Models, core.ModelInfo{Name: name, SizeVRAM: mm.SizeVRAM})
	}
	m.Version = o.version.fetch(ctx, o.base)
	return m, nil
}
