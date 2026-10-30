package backup

import (
	"archive/tar"
	"fmt"
	"io"
	"path"
	"slices"
	"strings"
	"unicode"

	"go.yaml.in/yaml/v3"
)

// ComposeRisk is one setting in an archived compose file that hands the
// container more of the host than an ordinary service gets.
type ComposeRisk struct {
	Stack, File, Service string
	// Kind is privileged, cap_add, device, host_bind, host_namespace,
	// security_opt or unparseable.
	Kind string
	// Detail is the setting as written, for example "SYS_ADMIN" or "/:/host".
	Detail string
}

const (
	riskPrivileged     = "privileged"
	riskCapAdd         = "cap_add"
	riskDevice         = "device"
	riskHostBind       = "host_bind"
	riskHostNamespace  = "host_namespace"
	riskSecurityOpt    = "security_opt"
	riskUnparseable    = "unparseable"
	maxRisksPerArchive = 500
)

// ScanComposeRisks lists the host-reaching settings of one compose file.
//
// The file is untrusted. It is only decoded into generic YAML values: nothing
// is interpolated (a ${VAR} stays as written, and a bind source that starts
// with one is reported because it could expand to anything), nothing is
// executed and no path is touched. Input beyond maxArchivedCompose is not
// parsed.
func ScanComposeRisks(stack, file string, src []byte) []ComposeRisk {
	add := func(risks *[]ComposeRisk, service, kind, detail string) {
		*risks = append(*risks, ComposeRisk{
			Stack: cleanText(stack), File: cleanText(file), Service: cleanText(service), Kind: kind, Detail: cleanText(detail),
		})
	}
	var risks []ComposeRisk
	if len(src) > maxArchivedCompose {
		add(&risks, "", riskUnparseable, "larger than the 4 MiB the scan reads")
		return risks
	}
	var doc any
	if err := yaml.Unmarshal(src, &doc); err != nil {
		add(&risks, "", riskUnparseable, firstLine(err.Error()))
		return risks
	}
	top, ok := doc.(map[string]any)
	if !ok {
		if doc != nil { // an empty file declares nothing
			add(&risks, "", riskUnparseable, "not a mapping")
		}
		return risks
	}
	services, _ := top["services"].(map[string]any)
	for _, name := range sortedKeys(services) {
		svc, _ := services[name].(map[string]any)
		scanService(svc, func(kind, detail string) { add(&risks, name, kind, detail) })
	}
	volumes, _ := top["volumes"].(map[string]any)
	for _, name := range sortedKeys(volumes) {
		vol, _ := volumes[name].(map[string]any)
		opts, _ := vol["driver_opts"].(map[string]any)
		if device, _ := opts["device"].(string); looksLikeHostPath(device) {
			add(&risks, "", riskHostBind, fmt.Sprintf("volume %s: device %s", name, device))
		}
	}
	return risks
}

func scanService(svc map[string]any, report func(kind, detail string)) {
	switch v := svc["privileged"].(type) {
	case bool:
		if v {
			report(riskPrivileged, "true")
		}
	case string:
		if strings.EqualFold(v, "true") || strings.Contains(v, "$") {
			report(riskPrivileged, v)
		}
	}
	for _, c := range stringList(svc["cap_add"]) {
		report(riskCapAdd, c)
	}
	for _, d := range stringList(svc["devices"]) {
		report(riskDevice, d)
	}
	for _, vol := range listOf(svc["volumes"]) {
		if detail, ok := hostBindOf(vol); ok {
			report(riskHostBind, detail)
		}
	}
	for _, key := range []string{"pid", "ipc", "network_mode", "userns_mode", "cgroup"} {
		if v, _ := svc[key].(string); v == "host" {
			report(riskHostNamespace, key+": host")
		}
	}
	for _, opt := range stringList(svc["security_opt"]) {
		if strings.Contains(opt, "unconfined") || opt == "label:disable" || opt == "label=disable" {
			report(riskSecurityOpt, opt)
		}
	}
}

// hostBindOf says whether one service volume entry binds a host path, and
// returns it as "source:target" (short syntax entries as written).
func hostBindOf(entry any) (string, bool) {
	switch v := entry.(type) {
	case string:
		source, _, hasTarget := strings.Cut(v, ":")
		return v, hasTarget && looksLikeHostPath(source)
	case map[string]any:
		if kind, _ := v["type"].(string); kind != "bind" {
			return "", false
		}
		source, _ := v["source"].(string)
		target, _ := v["target"].(string)
		return source + ":" + target, looksLikeHostPath(source)
	}
	return "", false
}

// looksLikeHostPath is true for a path outside the stack directory: absolute,
// under the home directory, above the stack directory, or a variable that
// could expand to any of those.
func looksLikeHostPath(p string) bool {
	return strings.HasPrefix(p, "/") || strings.HasPrefix(p, "~") || strings.HasPrefix(p, "$") ||
		p == ".." || strings.HasPrefix(p, "../")
}

func listOf(v any) []any {
	list, _ := v.([]any)
	return list
}

// stringList returns the string entries of a YAML list, scalars rendered as written.
func stringList(v any) []string {
	var out []string
	for _, item := range listOf(v) {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// maxRiskText caps each reported string, in runes.
const maxRiskText = 200

// cleanText drops control characters (a terminal escape sequence must not
// travel from an archive to the operator's screen) and caps the length.
func cleanText(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	if r := []rune(s); len(r) > maxRiskText {
		s = string(r[:maxRiskText])
	}
	return s
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// scanStackFile scans one archive entry when it is a compose file at the top
// of a stack directory (base/stacks/<stack>/<file>): a compose-named file, or
// any other YAML file there, which may be the stack's custom-named compose
// file. A YAML file that is not compose-named is only reported for real
// risks, never as unparseable. It returns nil for every other entry.
func scanStackFile(hdr *tar.Header, body io.Reader) ([]ComposeRisk, error) {
	if hdr.Typeflag != tar.TypeReg || !strings.HasPrefix(hdr.Name, stacksPrefix) {
		return nil, nil
	}
	stack, rel, ok := strings.Cut(strings.TrimPrefix(hdr.Name, stacksPrefix), "/")
	if !ok || stack == "" || rel == "" || strings.Contains(rel, "/") {
		return nil, nil
	}
	named := isComposeFileName(rel)
	if ext := path.Ext(rel); !named && ext != ".yml" && ext != ".yaml" {
		return nil, nil
	}
	src, err := io.ReadAll(io.LimitReader(body, maxArchivedCompose+1))
	if err != nil {
		return nil, err
	}
	risks := ScanComposeRisks(stack, rel, src)
	if !named {
		risks = slices.DeleteFunc(risks, func(r ComposeRisk) bool { return r.Kind == riskUnparseable })
	}
	return risks, nil
}

// sortRisks orders risks by stack and file, keeping the order within a file,
// and drops those past maxRisksPerArchive.
func sortRisks(risks []ComposeRisk) []ComposeRisk {
	slices.SortStableFunc(risks, func(a, b ComposeRisk) int {
		return strings.Compare(a.Stack+"\x00"+a.File, b.Stack+"\x00"+b.File)
	})
	if len(risks) > maxRisksPerArchive {
		risks = risks[:maxRisksPerArchive]
	}
	return risks
}

// isComposeFileName reports whether base names a compose file by the same
// globs the backup uses to protect them (see effectiveSkips).
func isComposeFileName(base string) bool {
	for _, pattern := range []string{"compose*.y*ml", "docker-compose*.y*ml"} {
		if ok, _ := path.Match(pattern, base); ok {
			return true
		}
	}
	return false
}
