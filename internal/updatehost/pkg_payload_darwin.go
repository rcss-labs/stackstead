package updatehost

import (
	"archive/tar"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/rcss-labs/stackstead/internal/update"
)

const pkgComponent = "stackstead-component.pkg"

func inspectPKGPayload(ctx context.Context, req payloadRequest, input *os.File) (payloadInspection, error) {
	var proof payloadInspection
	payload, err := os.CreateTemp("", "stackstead-pkg-payload-")
	if err != nil {
		return proof, err
	}
	defer os.Remove(payload.Name())
	defer payload.Close()
	var metadata, distribution []byte
	err = convertPayloadStream(ctx, input, "/usr/bin/bsdtar", []string{"-cf", "-", "--format=ustar", "@/dev/fd/3"}, 1<<30, func(r io.Reader) error {
		return readPKGEnvelope(r, func(role string, h *tar.Header, r io.Reader) error {
			switch role {
			case "Payload":
				if h.Size <= 0 || h.Size > 1<<30 {
					return update.ErrIncompatible
				}
				_, e := io.CopyN(payload, r, h.Size)
				return e
			case "Distribution":
				if h.Size <= 0 || h.Size > childMetadataLimit {
					return update.ErrIncompatible
				}
				var e error
				distribution, e = io.ReadAll(io.LimitReader(r, h.Size))
				return e
			case "PackageInfo":
				if h.Size <= 0 || h.Size > childMetadataLimit {
					return update.ErrIncompatible
				}
				var e error
				metadata, e = io.ReadAll(io.LimitReader(r, h.Size))
				return e
			default:
				_, e := io.CopyN(io.Discard, r, h.Size)
				return e
			}
		})
	})
	if err != nil {
		return proof, fmt.Errorf("pkg envelope: %w", err)
	}
	id, version, location, err := readPKGInfo(metadata)
	if err != nil || id != pkgIdentifier || version != req.Identity.Version || location != "/" {
		return proof, fmt.Errorf("pkg info %q %q %q: %w", id, version, location, errors.Join(err, update.ErrIncompatible))
	}
	if err = validatePKGDistribution(distribution, version); err != nil {
		return proof, err
	}
	if _, err = payload.Seek(0, 0); err != nil {
		return proof, err
	}
	err = convertPayloadStream(ctx, payload, "/usr/bin/bsdtar", []string{"-cf", "-", "--format=ustar", "@-"}, 2<<30, func(r io.Reader) error {
		var e error
		proof, e = inventoryTar(ctx, r, req, defaultPayloadBounds, true)
		return e
	})
	if err != nil {
		return proof, fmt.Errorf("pkg inventory: %w", err)
	}
	for _, entry := range proof.Entries {
		if entry.Metadata != nil {
			if !pkgPayloadDestination(entry.Metadata.Companion) || entry.Path != metadataPath(entry.Metadata.Companion) {
				return proof, update.ErrIncompatible
			}
			continue
		}
		if !pkgPayloadDestination(entry.Path) {
			return proof, fmt.Errorf("pkg destination %q: %w", entry.Path, update.ErrIncompatible)
		}
		if entry.Directory != pkgPayloadDirectory(entry.Path) {
			return proof, update.ErrIncompatible
		}
	}
	proof.Identifier = id
	proof.Version = version
	proof.InstallLocation = location
	return proof, nil
}
func readPKGEnvelope(source io.Reader, member func(string, *tar.Header, io.Reader) error) error {
	tr := tar.NewReader(source)
	seen := map[string]bool{}
	encoding := ""
	count := 0
	names := 0
	for {
		h, e := tr.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			return e
		}
		count++
		names += len(h.Name)
		if count > 64 || names > childMetadataLimit || len(h.Name) > 1024 || len(h.PAXRecords) != 0 || h.Format == tar.FormatPAX || h.Format == tar.FormatGNU || h.Linkname != "" || h.Size < 0 {
			return update.ErrIncompatible
		}
		if h.Name == "Resources/" || h.Name == "Resources" || h.Name == "Resources/en.lproj/" || h.Name == "Resources/en.lproj" {
			key := strings.TrimSuffix(h.Name, "/")
			if h.Typeflag != tar.TypeDir || h.Size != 0 || seen[key] {
				return update.ErrIncompatible
			}
			seen[key] = true
			continue
		}
		if h.Name == pkgComponent+"/" || h.Name == pkgComponent {
			if h.Typeflag != tar.TypeDir || h.Size != 0 || seen["component"] {
				return update.ErrIncompatible
			}
			seen["component"] = true
			continue
		}
		if h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeRegA {
			return update.ErrIncompatible
		}
		role := h.Name
		component := false
		for _, candidate := range []string{"Bom", "Payload", "PackageInfo", "Scripts"} {
			p := pkgComponent + "/" + candidate
			if h.Name == p || h.Name == p+p {
				mode := "canonical"
				if h.Name == p+p {
					mode = "duplicated"
				}
				if encoding != "" && encoding != mode {
					return update.ErrIncompatible
				}
				encoding = mode
				role = candidate
				component = true
				break
			}
		}
		if !component {
			switch h.Name {
			case "Distribution", "Resources/en.lproj/welcome.html", "Resources/en.lproj/conclusion.html", "Resources/welcome.html", "Resources/conclusion.html":
			default:
				return update.ErrIncompatible
			}
		}
		if seen[role] {
			return update.ErrIncompatible
		}
		seen[role] = true
		if h.Size > 1<<30 {
			return update.ErrIncompatible
		}
		if role != "Payload" && h.Size > childMetadataLimit {
			return update.ErrIncompatible
		}
		if e = member(role, h, tr); e != nil {
			return e
		}
	}
	if !seen["component"] || !seen["PackageInfo"] || !seen["Payload"] || !seen["Bom"] || !seen["Distribution"] {
		return update.ErrIncompatible
	}
	// The converter's stream has already been bounded. Reject nonzero trailing data.
	var b [4096]byte
	for {
		n, e := source.Read(b[:])
		for _, v := range b[:n] {
			if v != 0 {
				return update.ErrIncompatible
			}
		}
		if e == io.EOF {
			break
		}
		if e != nil {
			return e
		}
	}
	return nil
}
func readPKGInfo(b []byte) (identifier, version, location string, err error) {
	fail := func() (string, string, string, error) { return "", "", "", update.ErrIncompatible }
	if len(b) == 0 || len(b) > childMetadataLimit {
		return fail()
	}
	d := xml.NewDecoder(strings.NewReader(string(b)))
	stack := []string{}
	seen := map[string]bool{}
	closed := false
	tokens := 0
	empty := map[string]bool{"bundle-version": true, "upgrade-bundle": true, "update-bundle": true, "atomic-update-bundle": true, "strict-identifier": true, "relocate": true}
	for {
		token, e := d.Token()
		if e == io.EOF {
			break
		}
		if e != nil {
			return "", "", "", e
		}
		tokens++
		if tokens > 256 {
			return fail()
		}
		switch t := token.(type) {
		case xml.Directive:
			return fail()
		case xml.ProcInst:
			if t.Target != "xml" || seen["pkg-info"] {
				return fail()
			}
		case xml.CharData:
			if strings.TrimSpace(string(t)) != "" {
				return fail()
			}
		case xml.StartElement:
			name := t.Name.Local
			if closed || t.Name.Space != "" || strings.Contains(name, ":") || seen[name] {
				return fail()
			}
			parent := ""
			if len(stack) > 0 {
				parent = stack[len(stack)-1]
			}
			switch name {
			case "pkg-info":
				if parent != "" {
					return fail()
				}
			case "payload", "scripts":
				if parent != "pkg-info" {
					return fail()
				}
			case "postinstall":
				if parent != "scripts" {
					return fail()
				}
			default:
				if !empty[name] || parent != "pkg-info" {
					return fail()
				}
			}
			attrs := map[string]string{}
			for _, a := range t.Attr {
				if a.Name.Space != "" || strings.Contains(a.Name.Local, ":") {
					return fail()
				}
				if _, ok := attrs[a.Name.Local]; ok {
					return fail()
				}
				attrs[a.Name.Local] = a.Value
			}
			switch name {
			case "pkg-info":
				expected := map[string]string{"overwrite-permissions": "true", "relocatable": "false", "identifier": pkgIdentifier, "postinstall-action": "none", "format-version": "2", "install-location": "/", "auth": "root"}
				if len(attrs) != 9 {
					return fail()
				}
				for k, v := range expected {
					if attrs[k] != v {
						return fail()
					}
				}
				identifier = attrs["identifier"]
				version = attrs["version"]
				location = attrs["install-location"]
				if !nativeStableVersion.MatchString(version) || !pkgGeneratorVersion.MatchString(attrs["generator-version"]) {
					return fail()
				}
			case "payload":
				if len(attrs) != 2 {
					return fail()
				}
				for k, max := range map[string]uint64{"numberOfFiles": 4096, "installKBytes": 2 << 20} {
					v, ok := attrs[k]
					n, e := strconv.ParseUint(v, 10, 32)
					if !ok || e != nil || n == 0 || n > max {
						return fail()
					}
				}
			case "postinstall":
				if len(attrs) != 2 || attrs["file"] != "./postinstall" || attrs["timeout"] != "600" {
					return fail()
				}
			default:
				if len(attrs) != 0 {
					return fail()
				}
			}
			seen[name] = true
			stack = append(stack, name)
		case xml.EndElement:
			if len(stack) == 0 {
				return fail()
			}
			stack = stack[:len(stack)-1]
			if len(stack) == 0 {
				closed = true
			}
		}
	}
	for _, name := range []string{"pkg-info", "payload", "bundle-version", "upgrade-bundle", "update-bundle", "atomic-update-bundle", "strict-identifier", "relocate", "scripts", "postinstall"} {
		if !seen[name] {
			return fail()
		}
	}
	if !closed || len(stack) != 0 {
		return fail()
	}
	return identifier, version, location, nil
}

var pkgGeneratorVersion = regexp.MustCompile(`^InstallCmds-[0-9]+ \([A-Za-z0-9]+\)$`)

func validatePKGDistribution(b []byte, version string) error {
	if len(b) == 0 || len(b) > childMetadataLimit {
		return update.ErrIncompatible
	}
	attributes := map[string]string{
		"installer-gui-script": "minSpecVersion", "title": "", "welcome": "file mime-type", "conclusion": "file mime-type", "options": "customize require-scripts hostArchitectures rootVolumeOnly", "allowed-os-versions": "", "os-version": "min", "choices-outline": "", "line": "choice", "choice": "id visible", "pkg-ref": "id version onConclusion installKBytes updateKBytes", "bundle-version": "",
	}
	parents := map[string]string{"title": "installer-gui-script", "welcome": "installer-gui-script", "conclusion": "installer-gui-script", "options": "installer-gui-script", "allowed-os-versions": "installer-gui-script", "os-version": "allowed-os-versions", "choices-outline": "installer-gui-script", "line": "choices-outline line", "choice": "installer-gui-script", "pkg-ref": "installer-gui-script choice", "bundle-version": "pkg-ref"}
	type node struct {
		name string
		text strings.Builder
	}
	stack := []*node{}
	d := xml.NewDecoder(strings.NewReader(string(b)))
	rootSeen, rootClosed := false, false
	locators, options, tokens := 0, 0, 0
	for {
		token, e := d.Token()
		if e == io.EOF {
			break
		}
		if e != nil {
			return e
		}
		tokens++
		if tokens > 2048 {
			return update.ErrIncompatible
		}
		switch t := token.(type) {
		case xml.Directive:
			return update.ErrIncompatible
		case xml.ProcInst:
			if t.Target != "xml" || rootSeen {
				return update.ErrIncompatible
			}
		case xml.StartElement:
			name := t.Name.Local
			allowed, ok := attributes[name]
			if !ok || t.Name.Space != "" || rootClosed || len(stack) >= 16 {
				return update.ErrIncompatible
			}
			if len(stack) == 0 {
				if rootSeen || name != "installer-gui-script" {
					return update.ErrIncompatible
				}
				rootSeen = true
			} else {
				parent := stack[len(stack)-1].name
				if !strings.Contains(" "+parents[name]+" ", " "+parent+" ") {
					return update.ErrIncompatible
				}
			}
			attrs := map[string]string{}
			for _, a := range t.Attr {
				if a.Name.Space != "" || strings.Contains(a.Name.Local, ":") || !strings.Contains(" "+allowed+" ", " "+a.Name.Local+" ") {
					return update.ErrIncompatible
				}
				if _, ok := attrs[a.Name.Local]; ok {
					return update.ErrIncompatible
				}
				attrs[a.Name.Local] = a.Value
			}
			for key, value := range attrs {
				switch key {
				case "minSpecVersion":
					if value != "2" {
						return update.ErrIncompatible
					}
				case "id", "choice":
					if value != "default" && value != pkgIdentifier {
						return update.ErrIncompatible
					}
				case "version":
					if value != version {
						return update.ErrIncompatible
					}
				case "onConclusion":
					if value != "none" {
						return update.ErrIncompatible
					}
				case "customize":
					if value != "never" {
						return update.ErrIncompatible
					}
				case "require-scripts", "rootVolumeOnly", "visible":
					if value != "false" {
						return update.ErrIncompatible
					}
				case "hostArchitectures":
					if value != "x86_64,arm64" {
						return update.ErrIncompatible
					}
				case "file":
					if (name == "welcome" && value != "welcome.html") || (name == "conclusion" && value != "conclusion.html") {
						return update.ErrIncompatible
					}
				case "mime-type":
					if value != "text/html" {
						return update.ErrIncompatible
					}
				case "min":
					if value != "12.0" {
						return update.ErrIncompatible
					}
				case "installKBytes", "updateKBytes":
					n, e := strconv.ParseUint(value, 10, 32)
					if e != nil || n > 2<<20 {
						return update.ErrIncompatible
					}
				}
			}
			switch name {
			case "installer-gui-script":
				if attrs["minSpecVersion"] != "2" {
					return update.ErrIncompatible
				}
			case "pkg-ref":
				if attrs["id"] != pkgIdentifier {
					return update.ErrIncompatible
				}
			case "options":
				options++
				if attrs["customize"] != "never" || attrs["require-scripts"] != "false" {
					return update.ErrIncompatible
				}
			}
			stack = append(stack, &node{name: name})
		case xml.CharData:
			if len(stack) == 0 {
				if strings.TrimSpace(string(t)) != "" {
					return update.ErrIncompatible
				}
				continue
			}
			stack[len(stack)-1].text.Write(t)
		case xml.EndElement:
			if len(stack) == 0 {
				return update.ErrIncompatible
			}
			n := stack[len(stack)-1]
			text := strings.TrimSpace(n.text.String())
			if text != "" {
				switch n.name {
				case "pkg-ref":
					if text != "#"+pkgComponent {
						return update.ErrIncompatible
					}
					locators++
				case "title":
					if text != "Stackstead" {
						return update.ErrIncompatible
					}
				default:
					return update.ErrIncompatible
				}
			}
			stack = stack[:len(stack)-1]
			if len(stack) == 0 {
				rootClosed = true
			}
		}
	}
	if !rootSeen || !rootClosed || len(stack) != 0 || locators != 1 || options != 1 {
		return update.ErrIncompatible
	}
	return nil
}
