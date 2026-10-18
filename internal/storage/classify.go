package storage

import (
	"cmp"
	"maps"
	"math"
	"slices"
	"strings"

	"github.com/rcss-labs/stackstead/internal/engine"
)

const composeProjectLabel = "com.docker.compose.project"

// Classify joins storage lists and cached readings without I/O. Stacks contains
// registered stacks only; RegisteredProjects preserves label ownership even when
// Compose declarations are unknown. Negative and absent sizes become nil.
func Classify(in Inputs) Inventory {
	out := Inventory{Images: []Image{}, Volumes: []Volume{}, BuildCache: []BuildRecord{}, Containers: []ContainerLayer{}, TopContainers: []ContainerLayer{}, Totals: map[engine.DFKind]KindTotals{}, DeclarationsUnknown: []string{}}
	imageConsumers := map[string][]Consumer{}
	volumeConsumers := map[string][]Consumer{}
	imageDeclarations := map[string][]Declaration{}
	volumeDeclarations := map[string][]Declaration{}
	unknown := map[string]bool{}
	for _, stack := range in.Stacks {
		if !stack.Known {
			unknown[stack.Name] = true
			continue
		}
		for _, image := range stack.Images {
			ref := image.Reference
			if ref == "" && image.Built {
				ref = stack.Project + "-" + image.Service
			}
			if ref != "" {
				key := normalizeReference(ref)
				imageDeclarations[key] = append(imageDeclarations[key], Declaration{Stack: stack.Name, Service: image.Service, Ref: ref, StackState: stack.State})
			}
		}
		for _, volume := range stack.Volumes {
			volumeDeclarations[volume.Name] = append(volumeDeclarations[volume.Name], Declaration{Stack: stack.Name, Ref: volume.Name, StackState: stack.State})
		}
	}
	for name := range unknown {
		out.DeclarationsUnknown = append(out.DeclarationsUnknown, name)
	}
	slices.Sort(out.DeclarationsUnknown)
	dfImages := map[string]engine.DFImage{}
	if usageLoaded(in, engine.DFImages) {
		for _, image := range in.DF[engine.DFImages].Snapshot.Images {
			dfImages[image.ID] = image
		}
	}
	dfVolumes := map[string]engine.DFVolume{}
	if usageLoaded(in, engine.DFVolumes) {
		for _, volume := range in.DF[engine.DFVolumes].Snapshot.Volumes {
			dfVolumes[volume.Name] = volume
		}
	}
	dfContainers := map[string]engine.DFContainer{}
	if usageLoaded(in, engine.DFContainers) {
		for _, container := range in.DF[engine.DFContainers].Snapshot.Containers {
			dfContainers[container.ID] = container
		}
	}
	for _, container := range in.Containers {
		consumer := Consumer{ContainerID: container.ID, Name: container.Name, State: container.State, Project: container.Project(), Service: container.ComposeService(), Stack: in.RegisteredProjects[container.Project()]}
		if container.ImageID != "" {
			imageConsumers[container.ImageID] = append(imageConsumers[container.ImageID], consumer)
		}
		mounted := map[string]bool{}
		for _, mount := range container.Mounts {
			if mount.Type == "volume" && mount.Name != "" && !mounted[mount.Name] {
				volumeConsumers[mount.Name] = append(volumeConsumers[mount.Name], consumer)
				mounted[mount.Name] = true
			}
		}
		out.Containers = append(out.Containers, ContainerLayer{Consumer: consumer, SizeRw: knownSize(dfContainers[container.ID].SizeRw)})
	}
	for _, image := range in.Images {
		tags := []string{}
		for _, tag := range image.RepoTags {
			if tag != "" && tag != "<none>:<none>" {
				tags = append(tags, tag)
			}
		}
		slices.Sort(tags)
		row := Image{ID: image.ID, Tags: tags, Digests: slices.Clone(image.RepoDigests), Created: image.Created, Size: knownSize(&image.Size), Containers: sortedConsumers(imageConsumers[image.ID]), DeclaredBy: []Declaration{}, Dangling: len(tags) == 0, Status: "no-containers"}
		if usageLoaded(in, engine.DFImages) {
			usage := dfImages[image.ID]
			row.Size = knownSize(usage.Size)
			row.SharedSize = knownSize(usage.SharedSize)
		}
		if len(row.Containers) > 0 {
			row.Status = "in-use"
		} else if row.Dangling {
			row.Status = "dangling"
		}
		for _, ref := range append(slices.Clone(row.Tags), row.Digests...) {
			row.DeclaredBy = append(row.DeclaredBy, imageDeclarations[normalizeReference(ref)]...)
		}
		row.DeclaredBy = sortedDeclarations(row.DeclaredBy)
		out.Images = append(out.Images, row)
	}
	for _, volume := range in.Volumes {
		row := Volume{Name: volume.Name, Driver: volume.Driver, Scope: volume.Scope, Project: volume.Labels[composeProjectLabel], CreatedAt: volume.CreatedAt, Labels: maps.Clone(volume.Labels), SizesPending: !usageLoaded(in, engine.DFVolumes), Containers: sortedConsumers(volumeConsumers[volume.Name]), DeclaredBy: sortedDeclarations(volumeDeclarations[volume.Name]), Status: "unused"}
		if usage := dfVolumes[volume.Name].UsageData; usage != nil {
			row.Size = knownSize(&usage.Size)
			row.RefCount = knownSize(&usage.RefCount)
		}
		if len(row.Containers) > 0 || row.RefCount != nil && *row.RefCount > 0 {
			row.Status = "in-use"
		}
		if len(row.DeclaredBy) > 0 {
			row.Protection = &Protection{Reason: "stack", Stack: row.DeclaredBy[0].Stack}
		} else if stack, ok := in.RegisteredProjects[row.Project]; ok && row.Project != "" {
			row.Protection = &Protection{Reason: "stack", Stack: stack}
		}
		out.Volumes = append(out.Volumes, row)
	}
	if usageLoaded(in, engine.DFBuildCache) {
		for _, record := range in.DF[engine.DFBuildCache].Snapshot.BuildCache {
			record.Size = knownSize(record.Size)
			if record.LastUsedAt != nil {
				at := *record.LastUsedAt
				record.LastUsedAt = &at
			}
			out.BuildCache = append(out.BuildCache, record)
		}
	}
	slices.SortFunc(out.Images, func(a, b Image) int { return compareSize(a.Size, b.Size, imageName(a), imageName(b), a.ID, b.ID) })
	slices.SortFunc(out.Volumes, func(a, b Volume) int { return compareSize(a.Size, b.Size, a.Name, b.Name, a.Name, b.Name) })
	slices.SortFunc(out.BuildCache, func(a, b BuildRecord) int { return compareSize(a.Size, b.Size, a.ID, b.ID, a.ID, b.ID) })
	slices.SortFunc(out.Containers, func(a, b ContainerLayer) int {
		return compareSize(a.SizeRw, b.SizeRw, a.Name, b.Name, a.ContainerID, b.ContainerID)
	})
	out.TopContainers = append(out.TopContainers, out.Containers[:min(5, len(out.Containers))]...)
	out.Totals = inventoryTotals(in, out)
	return out
}

func usageLoaded(in Inputs, kind engine.DFKind) bool {
	reading := in.DF[kind]
	return !reading.LoadedAt.IsZero() && slices.Contains(reading.Snapshot.Kinds, kind)
}

func knownSize(value *int64) *int64 {
	if value == nil || *value < 0 {
		return nil
	}
	copy := *value
	return &copy
}

func sortedConsumers(consumers []Consumer) []Consumer {
	out := append([]Consumer{}, consumers...)
	slices.SortFunc(out, func(a, b Consumer) int {
		if c := cmp.Compare(a.Name, b.Name); c != 0 {
			return c
		}
		return cmp.Compare(a.ContainerID, b.ContainerID)
	})
	return out
}

func sortedDeclarations(declarations []Declaration) []Declaration {
	out := append([]Declaration{}, declarations...)
	slices.SortFunc(out, func(a, b Declaration) int {
		if c := cmp.Compare(a.Stack, b.Stack); c != 0 {
			return c
		}
		if c := cmp.Compare(a.Service, b.Service); c != 0 {
			return c
		}
		if c := cmp.Compare(a.Ref, b.Ref); c != 0 {
			return c
		}
		return cmp.Compare(a.StackState, b.StackState)
	})
	return slices.Compact(out)
}

func imageName(image Image) string {
	if len(image.Tags) > 0 {
		return image.Tags[0]
	}
	return image.ID
}

func compareSize(a, b *int64, aName, bName, aID, bID string) int {
	if a != nil && b == nil {
		return -1
	}
	if a == nil && b != nil {
		return 1
	}
	if a != nil && b != nil {
		if c := cmp.Compare(*b, *a); c != 0 {
			return c
		}
	}
	if c := cmp.Compare(aName, bName); c != 0 {
		return c
	}
	return cmp.Compare(aID, bID)
}

// normalizeReference expands Docker Hub's familiar names without treating a
// registry's port as an image tag. Digest references never gain :latest.
func normalizeReference(ref string) string {
	name, digest, hasDigest := strings.Cut(ref, "@")
	slash := strings.LastIndex(name, "/")
	if !hasDigest && strings.LastIndex(name, ":") <= slash {
		name += ":latest"
	}
	first, _, hasSlash := strings.Cut(name, "/")
	if !hasSlash || !strings.ContainsAny(first, ".:") && first != "localhost" {
		name = "docker.io/" + name
	}
	if strings.HasPrefix(name, "index.docker.io/") {
		name = "docker.io/" + strings.TrimPrefix(name, "index.docker.io/")
	}
	if strings.HasPrefix(name, "docker.io/") && !strings.Contains(strings.TrimPrefix(name, "docker.io/"), "/") {
		name = "docker.io/library/" + strings.TrimPrefix(name, "docker.io/")
	}
	if hasDigest {
		return name + "@" + digest
	}
	return name
}

// sizeSum refuses partial/overflowing totals; measured zero stays meaningful.
type sizeSum struct {
	value   int64
	unknown bool
}

func (s *sizeSum) add(value *int64) {
	if value == nil || *value < 0 {
		s.unknown = true
		return
	}
	if s.value > math.MaxInt64-*value {
		s.unknown = true
		return
	}
	s.value += *value
}
func (s sizeSum) result() *int64 {
	if s.unknown {
		return nil
	}
	return &s.value
}

func inventoryTotals(in Inputs, out Inventory) map[engine.DFKind]KindTotals {
	totals := map[engine.DFKind]KindTotals{}
	for _, kind := range []engine.DFKind{engine.DFImages, engine.DFContainers, engine.DFVolumes, engine.DFBuildCache} {
		reading := in.DF[kind]
		loaded := usageLoaded(in, kind)
		total := KindTotals{Refreshing: reading.Refreshing, Error: reading.Error}
		if loaded {
			at := reading.LoadedAt
			total.SizesAt = &at
		}
		size, reclaim := sizeSum{}, sizeSum{}
		switch kind {
		case engine.DFImages:
			total.Count = len(out.Images)
			for _, image := range out.Images {
				size.add(image.Size)
				if image.Status == "dangling" {
					if image.Size == nil || image.SharedSize == nil {
						reclaim.add(nil)
					} else {
						value := max(int64(0), *image.Size-*image.SharedSize)
						reclaim.add(&value)
					}
				}
			}
		case engine.DFVolumes:
			total.Count = len(out.Volumes)
			for _, volume := range out.Volumes {
				size.add(volume.Size)
				if volume.Status == "unused" && volume.Protection == nil {
					reclaim.add(volume.Size)
				}
			}
		case engine.DFContainers:
			total.Count = len(out.Containers)
			for _, container := range out.Containers {
				size.add(container.SizeRw)
			}
		case engine.DFBuildCache:
			total.Count = len(out.BuildCache)
			for _, record := range out.BuildCache {
				size.add(record.Size)
				if !record.InUse && !record.Shared {
					reclaim.add(record.Size)
				}
			}
		}
		if loaded || kind == engine.DFImages {
			total.Size = size.result()
		}
		if loaded && kind != engine.DFContainers {
			total.Reclaimable = reclaim.result()
		}
		totals[kind] = total
	}
	return totals
}
