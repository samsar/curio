package setup

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"

	"github.com/samsar/curio/internal/config"
)

// configHeader opens every config.yaml curio up writes.
const configHeader = `# curio's settings for this home. curio up wrote this file once and never
# edits it again: it is yours. A key left out takes its default (see
# docs/setup.md). embedding.model and embedding.dim are this home's for
# good: another embedding model needs a new home (curio up --fresh).
# After an edit, restart the daemon: curio daemon stop, and the next
# command starts it.
`

// configFile is what curio up writes of a config: the embedding model and
// its prompts, the writing model, and the addresses that differ from the
// built-in defaults.
type configFile struct {
	Daemon     *daemonSection    `yaml:"daemon,omitempty"`
	Embedding  embeddingSection  `yaml:"embedding"`
	Generation generationSection `yaml:"generation"`
}

type daemonSection struct {
	Listen string `yaml:"listen"`
}

type embeddingSection struct {
	Model          string `yaml:"model"`
	Dim            int    `yaml:"dim"`
	BaseURL        string `yaml:"base_url,omitempty"`
	DocumentPrefix string `yaml:"document_prefix"`
	QueryPrefix    string `yaml:"query_prefix"`
}

type generationSection struct {
	Model   string `yaml:"model"`
	BaseURL string `yaml:"base_url,omitempty"`
}

// renderConfig is cfg as the config.yaml curio up writes: the header, then
// the keys it decides, and the addresses where they differ from
// config.Default().
func renderConfig(cfg config.Config) ([]byte, error) {
	builtIn := config.Default()
	f := configFile{
		Embedding: embeddingSection{
			Model: cfg.Embedding.Model, Dim: cfg.Embedding.Dim,
			DocumentPrefix: cfg.Embedding.DocumentPrefix, QueryPrefix: cfg.Embedding.QueryPrefix,
		},
		Generation: generationSection{Model: cfg.Generation.Model},
	}
	if cfg.Daemon.Listen != builtIn.Daemon.Listen {
		f.Daemon = &daemonSection{Listen: cfg.Daemon.Listen}
	}
	if cfg.Embedding.BaseURL != builtIn.Embedding.BaseURL {
		f.Embedding.BaseURL = cfg.Embedding.BaseURL
	}
	if cfg.Generation.BaseURL != builtIn.Generation.BaseURL {
		f.Generation.BaseURL = cfg.Generation.BaseURL
	}
	var buf bytes.Buffer
	buf.WriteString(configHeader)
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(f); err != nil {
		return nil, fmt.Errorf("encode config.yaml: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("encode config.yaml: %w", err)
	}
	return buf.Bytes(), nil
}

// writeConfig writes the config.yaml of a home that has none: its
// marker's embedding model and width, that model's prompts, and the
// writing model picked. It is written only while there is still none.
func (w *world) writeConfig(ctx context.Context, ui UI, hs homeState) error {
	cfg := w.baseConfig(hs)
	cfg.Embedding.Model, cfg.Embedding.Dim = hs.meta.EmbeddingModel, hs.meta.EmbeddingDim
	if p, known := modelPrompts(cfg.Embedding.Model); known {
		cfg.Embedding.DocumentPrefix, cfg.Embedding.QueryPrefix = p.document, p.query
	} else {
		cfg.Embedding.DocumentPrefix, cfg.Embedding.QueryPrefix = "", ""
		ui.Warn(fmt.Sprintf("curio doesn't know the prompts %s expects: before importing, set "+
			"embedding.document_prefix and embedding.query_prefix in %s as its model card says",
			cfg.Embedding.Model, hs.home.ConfigPath()))
	}
	cfg.Generation.Model = w.generationToWrite(ctx, hs)
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("the config.yaml curio up would write is invalid: %w", err)
	}
	data, err := renderConfig(cfg)
	if err != nil {
		return err
	}
	err = writeNewFile(hs.home.ConfigPath(), data)
	switch {
	case errors.Is(err, fs.ErrExist):
		ui.Warn(hs.home.ConfigPath() + " appeared meanwhile; leaving it as it is")
		return nil
	case err != nil:
		return err
	}
	ui.Info("wrote " + hs.home.ConfigPath())
	return nil
}

// writeNewFile writes data to path, 0600, only while nothing is there,
// and atomically: a temp file in the same directory, synced, then linked
// into place. A link, unlike a rename, fails when path exists
// (fs.ErrExist) rather than replacing what someone else wrote meanwhile.
func writeNewFile(path string, data []byte) (err error) {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	defer func() {
		if rmErr := os.Remove(tmp.Name()); rmErr != nil && err == nil {
			err = fmt.Errorf("remove the temp file %s: %w", tmp.Name(), rmErr)
		}
	}()
	_, err = tmp.Write(data)
	if err == nil {
		err = tmp.Sync()
	}
	if err = errors.Join(err, tmp.Close()); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := os.Link(tmp.Name(), path); err != nil {
		return fmt.Errorf("put %s in place: %w", path, err)
	}
	return nil
}

// listenOf is the host:port of a --daemon-url, for the daemon.listen a
// new config.yaml gets; empty when there is none.
func listenOf(daemonURL string) string {
	if daemonURL == "" {
		return ""
	}
	u, err := url.Parse(daemonURL)
	if err != nil {
		return ""
	}
	return u.Host
}
