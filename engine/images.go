package engine

import (
	"context"
	"time"
)

// Image is one entry from `container image ls --format json`. The reference the
// UI shows (and the create form's presence-check uses) lives at
// configuration.name; the rest is metadata for the picker.
type Image struct {
	Reference string    `json:"reference"` // e.g. "docker.io/library/alpine:latest"
	Digest    string    `json:"digest"`
	Size      int64     `json:"size"`
	Created   time.Time `json:"created"`
}

// rawImage mirrors the captured `image ls --format json` shape (create spec
// §8.3): {configuration:{name, creationDate, descriptor{digest,size}}, id,
// variants[]}. variants is ignored — the picker needs only the ref + metadata.
type rawImage struct {
	Configuration struct {
		Name         string    `json:"name"`
		CreationDate time.Time `json:"creationDate"`
		Descriptor   struct {
			Digest string `json:"digest"`
			Size   int64  `json:"size"`
		} `json:"descriptor"`
	} `json:"configuration"`
}

// ImageList returns locally-present images for the create picker / presence check.
func (e *CLIEngine) ImageList(ctx context.Context) ([]Image, error) {
	var raw []rawImage
	if err := e.runJSON(ctx, &raw, "image", "ls", "--format", "json"); err != nil {
		return nil, err
	}
	out := make([]Image, 0, len(raw))
	for _, r := range raw {
		out = append(out, Image{
			Reference: r.Configuration.Name,
			Digest:    r.Configuration.Descriptor.Digest,
			Size:      r.Configuration.Descriptor.Size,
			Created:   r.Configuration.CreationDate,
		})
	}
	return out, nil
}
