package storage

import (
	"reflect"
	"testing"
	"time"

	"github.com/rcss-labs/stackstead/internal/engine"
)

func classifyBytes(n int64) *int64 { return &n }

func classifyReading(kind engine.DFKind, snapshot engine.DFSnapshot) map[engine.DFKind]DFReading {
	snapshot.Kinds = []engine.DFKind{kind}
	return map[engine.DFKind]DFReading{kind: {Snapshot: snapshot, LoadedAt: time.Unix(100, 0)}}
}

func TestClassifyImageStatusUsesIDInEveryState(t *testing.T) {
	for _, tc := range []struct {
		name, state, imageID string
		tags                 []string
		want                 string
	}{
		{"running", "running", "old", []string{"nginx:latest"}, "in-use"},
		{"exited", "exited", "old", nil, "in-use"},
		{"created", "created", "old", nil, "in-use"},
		{"moved tag", "running", "new", []string{"nginx:latest"}, "no-containers"},
		{"dangling", "", "", nil, "dangling"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Classify(Inputs{Images: []engine.Image{{ID: "old", RepoTags: tc.tags, Size: 50}}, Containers: []engine.Container{{ID: "c", Name: "web", ImageID: tc.imageID, Image: "nginx:latest", State: tc.state}}})
			if len(got.Images) != 1 {
				t.Fatalf("images=%v", got.Images)
			}
			image := got.Images[0]
			if image.Status != tc.want {
				t.Fatalf("status=%q want %q", image.Status, tc.want)
			}
			if tc.want == "in-use" && (len(image.Containers) != 1 || image.Containers[0].State != tc.state) {
				t.Fatalf("consumers=%v", image.Containers)
			}
		})
	}
}

func TestClassifyDeclarationsAndReferenceNormalization(t *testing.T) {
	for _, tc := range []struct {
		name, tag, reference string
		built                bool
		want                 bool
	}{
		{"short", "nginx", "docker.io/library/nginx:latest", false, true},
		{"reverse", "docker.io/library/nginx:latest", "nginx", false, true},
		{"index alias", "index.docker.io/library/nginx", "nginx:latest", false, true},
		{"namespace", "docker.io/team/web", "team/web:latest", false, true},
		{"port", "localhost:5000/web", "localhost:5000/web:latest", false, true},
		{"digest", "nginx@sha256:abc", "docker.io/library/nginx@sha256:abc", false, true},
		{"different tag", "nginx:1", "nginx:2", false, false},
		{"different registry", "example.com/nginx", "nginx", false, false},
		{"default build", "media-web:latest", "", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			declarations := []StackDeclarations{{Name: "stack", Project: "media", State: "stopped", Known: true, Images: []DeclaredImage{{Service: "web", Reference: tc.reference, Built: tc.built}}}}
			got := Classify(Inputs{Images: []engine.Image{{ID: "i", RepoTags: []string{tc.tag}}}, Stacks: declarations})
			if len(got.Images) != 1 {
				t.Fatalf("images=%v", got.Images)
			}
			image := got.Images[0]
			if (len(image.DeclaredBy) > 0) != tc.want {
				t.Fatalf("declarations=%v want match=%v", image.DeclaredBy, tc.want)
			}
			if tc.want {
				if image.DeclaredBy[0].StackState != "stopped" || image.DeclaredBy[0].Stack != "stack" {
					t.Fatalf("declaration=%+v", image.DeclaredBy[0])
				}
				declarations[0].State = "running"
				next := Classify(Inputs{Images: []engine.Image{{ID: "i", RepoTags: []string{tc.tag}}}, Stacks: declarations})
				if next.Images[0].DeclaredBy[0].StackState != "running" {
					t.Fatal("stack state frozen")
				}
			}
		})
	}
}

func TestClassifyVolumeConsumersAndProtection(t *testing.T) {
	for _, tc := range []struct {
		name                               string
		mount                              engine.Mount
		refs                               int64
		declared, external, unknown, label bool
		wantStatus                         string
		protected                          bool
	}{
		{name: "stopped mount", mount: engine.Mount{Type: "volume", Name: "data", Source: "other"}, wantStatus: "in-use"},
		{name: "bind ignored", mount: engine.Mount{Type: "bind", Name: "data"}, wantStatus: "unused"},
		{name: "ref count", refs: 1, wantStatus: "in-use"},
		{name: "stopped stack", declared: true, wantStatus: "unused", protected: true},
		{name: "external declaration", declared: true, external: true, wantStatus: "unused", protected: true},
		{name: "unknown label", unknown: true, label: true, wantStatus: "unused", protected: true},
		{name: "unregistered label", label: true, wantStatus: "unused"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := Inputs{Volumes: []engine.Volume{{Name: "data"}}, Containers: []engine.Container{{ID: "c", Name: "db", State: "exited", Labels: map[string]string{"com.docker.compose.project": "media", "com.docker.compose.service": "db"}, Mounts: []engine.Mount{tc.mount}}}, DF: classifyReading(engine.DFVolumes, engine.DFSnapshot{Volumes: []engine.DFVolume{{Name: "data", UsageData: &engine.DFVolumeUsage{Size: 100, RefCount: tc.refs}}}})}
			if tc.label {
				in.Volumes[0].Labels = map[string]string{"com.docker.compose.project": "media"}
			}
			if tc.declared || tc.unknown {
				in.RegisteredProjects = map[string]string{"media": "stack"}
				in.Stacks = []StackDeclarations{{Name: "stack", Project: "media", State: "stopped", Known: !tc.unknown}}
			}
			if tc.declared {
				in.Stacks[0].Volumes = []DeclaredVolume{{Name: "data", External: tc.external}}
			}
			got := Classify(in)
			if len(got.Volumes) != 1 {
				t.Fatalf("volumes=%v", got.Volumes)
			}
			v := got.Volumes[0]
			if v.Status != tc.wantStatus || (v.Protection != nil) != tc.protected {
				t.Fatalf("volume=%+v", v)
			}
			if tc.protected && (v.Protection.Reason != "stack" || v.Protection.Stack != "stack") {
				t.Fatalf("protection=%+v", v.Protection)
			}
			if tc.name == "stopped mount" && (len(v.Containers) != 1 || v.Containers[0].State != "exited" || v.Containers[0].Stack != "") {
				t.Fatalf("consumers=%v", v.Containers)
			}
			if tc.unknown && !reflect.DeepEqual(got.DeclarationsUnknown, []string{"stack"}) {
				t.Fatalf("unknown=%v", got.DeclarationsUnknown)
			}
		})
	}
}

func TestClassifyTotalsAndTopContainers(t *testing.T) {
	in := Inputs{Images: []engine.Image{{ID: "used"}, {ID: "dangling"}, {ID: "tagged", RepoTags: []string{"app"}}}, Volumes: []engine.Volume{{Name: "unused"}, {Name: "protected", Labels: map[string]string{"com.docker.compose.project": "media"}}, {Name: "used"}}, RegisteredProjects: map[string]string{"media": "stack"}, DF: map[engine.DFKind]DFReading{}}
	snapshots := map[engine.DFKind]engine.DFSnapshot{
		engine.DFImages:     {Images: []engine.DFImage{{ID: "used", Size: classifyBytes(100), SharedSize: classifyBytes(20)}, {ID: "dangling", Size: classifyBytes(80), SharedSize: classifyBytes(30)}, {ID: "tagged", Size: classifyBytes(20), SharedSize: classifyBytes(0)}}},
		engine.DFVolumes:    {Volumes: []engine.DFVolume{{Name: "unused", UsageData: &engine.DFVolumeUsage{Size: 10}}, {Name: "protected", UsageData: &engine.DFVolumeUsage{Size: 20}}, {Name: "used", UsageData: &engine.DFVolumeUsage{Size: 30, RefCount: 1}}}},
		engine.DFBuildCache: {BuildCache: []engine.DFBuildCacheRecord{{ID: "free", Size: classifyBytes(7)}, {ID: "used", Size: classifyBytes(11), InUse: true}, {ID: "shared", Size: classifyBytes(13), Shared: true}}},
		engine.DFContainers: {Containers: []engine.DFContainer{{ID: "1", SizeRw: classifyBytes(1)}, {ID: "2", SizeRw: classifyBytes(2)}, {ID: "3", SizeRw: classifyBytes(3)}, {ID: "4", SizeRw: classifyBytes(4)}, {ID: "5", SizeRw: classifyBytes(5)}, {ID: "6", SizeRw: classifyBytes(6)}}},
	}
	for kind, snapshot := range snapshots {
		in.DF[kind] = classifyReading(kind, snapshot)[kind]
	}
	for _, id := range []string{"1", "2", "3", "4", "5", "6"} {
		in.Containers = append(in.Containers, engine.Container{ID: id, Name: id, ImageID: "used", Labels: map[string]string{"com.docker.compose.project": "media"}})
	}
	got := Classify(in)
	for _, tc := range []struct {
		kind          engine.DFKind
		count         int
		size, reclaim int64
	}{{engine.DFImages, 3, 200, 50}, {engine.DFVolumes, 3, 60, 10}, {engine.DFBuildCache, 3, 31, 7}, {engine.DFContainers, 6, 21, -1}} {
		t.Run(string(tc.kind), func(t *testing.T) {
			totals := got.Totals[tc.kind]
			if totals.Count != tc.count || totals.Size == nil || *totals.Size != tc.size || totals.SizesAt == nil || !totals.SizesAt.Equal(time.Unix(100, 0)) {
				t.Fatalf("totals=%+v", totals)
			}
			if tc.reclaim < 0 {
				if totals.Reclaimable != nil {
					t.Fatalf("container reclaim=%v", totals.Reclaimable)
				}
			} else if totals.Reclaimable == nil || *totals.Reclaimable != tc.reclaim {
				t.Fatalf("reclaim=%v want %d", totals.Reclaimable, tc.reclaim)
			}
		})
	}
	if len(got.TopContainers) != 5 || got.TopContainers[0].Name != "6" || got.TopContainers[4].Name != "2" || got.TopContainers[0].Stack != "stack" {
		t.Fatalf("top=%+v", got.TopContainers)
	}
}

func TestClassifyUnknownAndProvisionalSizes(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		loaded               bool
		size, shared         *int64
		wantSize, wantShared *int64
		wantPending          bool
	}{
		{"before df", false, nil, nil, classifyBytes(12), nil, true},
		{"zero", true, classifyBytes(0), classifyBytes(0), classifyBytes(0), classifyBytes(0), false},
		{"negative", true, classifyBytes(-1), classifyBytes(-1), nil, nil, false},
		{"missing", true, nil, nil, nil, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := Inputs{Images: []engine.Image{{ID: "i", Size: 12}}, Volumes: []engine.Volume{{Name: "v"}}, Containers: []engine.Container{{ID: "c", Mounts: []engine.Mount{{Type: "volume", Name: "v"}}}}}
			if tc.loaded {
				in.DF = classifyReading(engine.DFImages, engine.DFSnapshot{Images: []engine.DFImage{{ID: "i", Size: tc.size, SharedSize: tc.shared}}})
				in.DF[engine.DFVolumes] = classifyReading(engine.DFVolumes, engine.DFSnapshot{})[engine.DFVolumes]
			}
			got := Classify(in)
			if len(got.Images) != 1 || len(got.Volumes) != 1 {
				t.Fatalf("inventory=%+v", got)
			}
			if !reflect.DeepEqual(got.Images[0].Size, tc.wantSize) || !reflect.DeepEqual(got.Images[0].SharedSize, tc.wantShared) {
				t.Fatalf("image=%+v", got.Images[0])
			}
			if got.Volumes[0].Size != nil || got.Volumes[0].SizesPending != tc.wantPending || got.Volumes[0].Status != "in-use" {
				t.Fatalf("volume=%+v", got.Volumes[0])
			}
			if !tc.loaded && (got.Images[0].SharedSize != nil || got.Totals[engine.DFImages].Reclaimable != nil || got.Totals[engine.DFVolumes].Size != nil) {
				t.Fatal("unloaded usage presented as measured")
			}
		})
	}
}

func TestClassifySortingAndUnknownTotals(t *testing.T) {
	in := Inputs{Images: []engine.Image{{ID: "unknown", RepoTags: []string{"unknown"}, Size: -1}, {ID: "z", RepoTags: []string{"z"}, Size: 10}, {ID: "a", RepoTags: []string{"a"}, Size: 10}, {ID: "zero", RepoTags: []string{"zero"}, Size: 0}}, Volumes: []engine.Volume{{Name: "z"}, {Name: "a"}, {Name: "unknown"}}, DF: classifyReading(engine.DFVolumes, engine.DFSnapshot{Volumes: []engine.DFVolume{{Name: "a", UsageData: &engine.DFVolumeUsage{Size: 0}}, {Name: "z", UsageData: &engine.DFVolumeUsage{Size: 0}}}}), Stacks: []StackDeclarations{{Name: "z"}, {Name: "a"}}}
	got := Classify(in)
	var images, volumes []string
	for _, v := range got.Images {
		images = append(images, v.ID)
	}
	for _, v := range got.Volumes {
		volumes = append(volumes, v.Name)
	}
	if !reflect.DeepEqual(images, []string{"a", "z", "zero", "unknown"}) || !reflect.DeepEqual(volumes, []string{"a", "z", "unknown"}) {
		t.Fatalf("images=%v volumes=%v", images, volumes)
	}
	if got.Totals[engine.DFImages].Size != nil || got.Totals[engine.DFVolumes].Size != nil || got.Totals[engine.DFVolumes].Reclaimable != nil {
		t.Fatalf("unknown totals=%+v", got.Totals)
	}
	if !reflect.DeepEqual(got.DeclarationsUnknown, []string{"a", "z"}) {
		t.Fatalf("unknown=%v", got.DeclarationsUnknown)
	}
}

func TestClassifyReadingMetadataAndEmptyKinds(t *testing.T) {
	for _, tc := range []struct {
		name   string
		kinds  []engine.DFKind
		at     time.Time
		loaded bool
	}{
		{name: "no timestamp", kinds: []engine.DFKind{engine.DFVolumes}},
		{name: "wrong kind", kinds: []engine.DFKind{engine.DFImages}, at: time.Unix(100, 0)},
		{name: "measured empty", kinds: []engine.DFKind{engine.DFVolumes}, at: time.Unix(100, 0), loaded: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Classify(Inputs{DF: map[engine.DFKind]DFReading{engine.DFVolumes: {Snapshot: engine.DFSnapshot{Kinds: tc.kinds}, LoadedAt: tc.at, Refreshing: true, Error: "last read failed"}}})
			total := got.Totals[engine.DFVolumes]
			if total.Refreshing != true || total.Error != "last read failed" || (total.SizesAt != nil) != tc.loaded {
				t.Fatalf("metadata=%+v", total)
			}
			if tc.loaded {
				if total.Size == nil || *total.Size != 0 || total.Reclaimable == nil || *total.Reclaimable != 0 {
					t.Fatalf("empty totals=%+v", total)
				}
			} else if total.Size != nil || total.Reclaimable != nil {
				t.Fatalf("unloaded totals=%+v", total)
			}
		})
	}
}

func TestClassifyBuildCacheUnknownSizesAndOrdering(t *testing.T) {
	for _, tc := range []struct {
		name          string
		inUse, shared bool
		wantReclaim   *int64
	}{
		{name: "unused unknown", wantReclaim: nil},
		{name: "used unknown", inUse: true, wantReclaim: classifyBytes(10)},
		{name: "shared unknown", shared: true, wantReclaim: classifyBytes(10)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Classify(Inputs{DF: classifyReading(engine.DFBuildCache, engine.DFSnapshot{BuildCache: []engine.DFBuildCacheRecord{{ID: "unknown", Size: classifyBytes(-1), InUse: tc.inUse, Shared: tc.shared}, {ID: "b", Size: classifyBytes(5)}, {ID: "a", Size: classifyBytes(5)}, {ID: "zero", Size: classifyBytes(0)}}})})
			ids := []string{}
			for _, record := range got.BuildCache {
				ids = append(ids, record.ID)
			}
			if !reflect.DeepEqual(ids, []string{"a", "b", "zero", "unknown"}) || got.BuildCache[3].Size != nil {
				t.Fatalf("records=%+v", got.BuildCache)
			}
			total := got.Totals[engine.DFBuildCache]
			if total.Size != nil || !reflect.DeepEqual(total.Reclaimable, tc.wantReclaim) {
				t.Fatalf("totals=%+v", total)
			}
		})
	}
}

func TestClassifyVolumeUsageUnknownZeroAndStoppedConsumer(t *testing.T) {
	for _, tc := range []struct {
		name               string
		usage              *engine.DFVolumeUsage
		wantSize, wantRefs *int64
	}{
		{name: "missing"},
		{name: "negative", usage: &engine.DFVolumeUsage{Size: -1, RefCount: -1}},
		{name: "zero", usage: &engine.DFVolumeUsage{}, wantSize: classifyBytes(0), wantRefs: classifyBytes(0)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Classify(Inputs{Volumes: []engine.Volume{{Name: "data"}}, Containers: []engine.Container{{ID: "c", Name: "db", State: "exited", Labels: map[string]string{composeProjectLabel: "media"}, Mounts: []engine.Mount{{Type: "volume", Name: "data"}, {Type: "volume", Name: "data"}}}}, RegisteredProjects: map[string]string{"media": "stack"}, DF: classifyReading(engine.DFVolumes, engine.DFSnapshot{Volumes: []engine.DFVolume{{Name: "data", UsageData: tc.usage}}})})
			v := got.Volumes[0]
			if !reflect.DeepEqual(v.Size, tc.wantSize) || !reflect.DeepEqual(v.RefCount, tc.wantRefs) || v.SizesPending || v.Status != "in-use" || len(v.Containers) != 1 || v.Containers[0].Stack != "stack" {
				t.Fatalf("volume=%+v", v)
			}
		})
	}
}

func TestClassifyDigestDeclarationsDeduplicateAndKeepInputs(t *testing.T) {
	in := Inputs{Images: []engine.Image{{ID: "i", Size: 10, RepoTags: []string{"nginx:latest", "docker.io/library/nginx"}, RepoDigests: []string{"docker.io/library/nginx@sha256:abc"}}}, Stacks: []StackDeclarations{{Name: "tags", Known: true, Images: []DeclaredImage{{Service: "web", Reference: "nginx"}}}, {Name: "digests", Known: true, Images: []DeclaredImage{{Service: "web", Reference: "nginx@sha256:abc"}}}}, Volumes: []engine.Volume{{Name: "v", Labels: map[string]string{composeProjectLabel: "media"}}}}
	got := Classify(in)
	if len(got.Images[0].DeclaredBy) != 2 || got.Images[0].DeclaredBy[0].Stack != "digests" || got.Images[0].DeclaredBy[1].Stack != "tags" {
		t.Fatalf("declarations=%+v", got.Images[0].DeclaredBy)
	}
	got.Images[0].Tags[0] = "changed"
	got.Images[0].Digests[0] = "changed"
	got.Volumes[0].Labels[composeProjectLabel] = "changed"
	if in.Images[0].RepoTags[0] != "nginx:latest" || in.Images[0].RepoDigests[0] != "docker.io/library/nginx@sha256:abc" || in.Volumes[0].Labels[composeProjectLabel] != "media" {
		t.Fatal("classification mutated input")
	}
}
