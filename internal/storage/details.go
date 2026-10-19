package storage

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/rcss-labs/stackstead/internal/engine"
)

type ImageDetail struct {
	Image
	Architecture, OS string
	Layers           int
	Labels           map[string]string
}
type VolumeDetail struct {
	Volume
	Mountpoint string
}

var imagePathPattern = regexp.MustCompile(`^[a-fA-F0-9]{12,64}$`)

// ImageDetails resolves an immutable image id before inspecting. Joining stays
// here so HTTP callers use the same ownership and cached size data as lists.
func (s *Service) ImageDetails(ctx context.Context, id string) (ImageDetail, error) {
	if !imagePathPattern.MatchString(id) {
		return ImageDetail{}, invalidField("id", "invalid image id")
	}
	inv, err := s.Inventory(ctx)
	if err != nil {
		return ImageDetail{}, err
	}
	var match *Image
	for _, i := range inv.Images {
		if strings.HasPrefix(strings.TrimPrefix(i.ID, "sha256:"), strings.ToLower(id)) {
			if match != nil {
				return ImageDetail{}, fmt.Errorf("%w: ambiguous image id", ErrConflict)
			}
			item := i
			match = &item
		}
	}
	if match == nil {
		return ImageDetail{}, engine.ErrImageNotFound
	}
	inspector, ok := s.cfg.Engine.(interface {
		InspectImage(context.Context, string) (engine.ImageInspect, error)
	})
	if !ok {
		return ImageDetail{}, ErrUnavailable
	}
	inspect, err := inspector.InspectImage(ctx, match.ID)
	if err != nil {
		return ImageDetail{}, err
	}
	return ImageDetail{Image: *match, Architecture: inspect.Architecture, OS: inspect.OS, Layers: inspect.LayerCount, Labels: inspect.Labels}, nil
}
func (s *Service) VolumeDetails(ctx context.Context, name string) (VolumeDetail, error) {
	if !volumeNamePattern.MatchString(name) {
		return VolumeDetail{}, invalidField("name", "invalid volume name")
	}
	inv, err := s.Inventory(ctx)
	if err != nil {
		return VolumeDetail{}, err
	}
	for _, v := range inv.Volumes {
		if v.Name == name {
			inspector, ok := s.cfg.Engine.(interface {
				InspectVolume(context.Context, string) (engine.VolumeInspect, error)
			})
			if !ok {
				return VolumeDetail{}, ErrUnavailable
			}
			inspect, err := inspector.InspectVolume(ctx, name)
			if err != nil {
				return VolumeDetail{}, err
			}
			v.Labels = inspect.Labels
			return VolumeDetail{Volume: v, Mountpoint: inspect.Mountpoint}, nil
		}
	}
	return VolumeDetail{}, engine.ErrVolumeNotFound
}
