package updatehost

import (
	"strings"
	"testing"
)

const observedPKGInfo = `<?xml version="1.0" encoding="utf-8"?><pkg-info overwrite-permissions="true" relocatable="false" identifier="io.github.rcss-labs.stackstead" postinstall-action="none" version="1.2.3" format-version="2" generator-version="InstallCmds-883 (26A434)" install-location="/" auth="root"><payload numberOfFiles="17" installKBytes="14890"/><bundle-version/><upgrade-bundle/><update-bundle/><atomic-update-bundle/><strict-identifier/><relocate/><scripts><postinstall file="./postinstall" timeout="600"/></scripts></pkg-info>`

func TestPKGInfoRejectsUnsupportedNativeSemantics(t *testing.T) {
	for _, bad := range []string{"", "relocatable", "overwrite", "action", "format", "auth", "preinstall", "script-traversal", "script-attribute", "duplicate-attribute", "namespaced-attribute", "bundle-modifier", "relocate-modifier", "wrong-parent", "duplicate-payload", "missing-payload", "duplicate-scripts", "missing-postinstall", "timeout", "payload-unknown"} {
		t.Run(bad, func(t *testing.T) {
			s := observedPKGInfo
			switch bad {
			case "relocatable":
				s = strings.Replace(s, `relocatable="false"`, `relocatable="true"`, 1)
			case "overwrite":
				s = strings.Replace(s, `overwrite-permissions="true"`, `overwrite-permissions="false"`, 1)
			case "action":
				s = strings.Replace(s, `postinstall-action="none"`, `postinstall-action="restart"`, 1)
			case "format":
				s = strings.Replace(s, `format-version="2"`, `format-version="3"`, 1)
			case "auth":
				s = strings.Replace(s, `auth="root"`, `auth="none"`, 1)
			case "preinstall":
				s = strings.Replace(s, "</pkg-info>", `<preinstall file="../../outside" script="unexpected" script="duplicate"/></pkg-info>`, 1)
			case "script-traversal":
				s = strings.Replace(s, `file="./postinstall"`, `file="../../outside"`, 1)
			case "script-attribute":
				s = strings.Replace(s, `timeout="600"`, `timeout="600" script="unexpected"`, 1)
			case "duplicate-attribute":
				s = strings.Replace(s, `timeout="600"`, `timeout="600" timeout="600"`, 1)
			case "namespaced-attribute":
				s = strings.Replace(s, `timeout="600"`, `timeout="600" xmlns:x="urn:x" x:script="unexpected"`, 1)
			case "bundle-modifier":
				s = strings.Replace(s, "<bundle-version/>", `<bundle-version version="other"/>`, 1)
			case "relocate-modifier":
				s = strings.Replace(s, "<relocate/>", `<relocate path="/elsewhere"/>`, 1)
			case "wrong-parent":
				s = strings.Replace(s, "<bundle-version/>", `<bundle-version><payload numberOfFiles="1" installKBytes="1"/></bundle-version>`, 1)
			case "duplicate-payload":
				s = strings.Replace(s, "<bundle-version/>", `<payload numberOfFiles="1" installKBytes="1"/><bundle-version/>`, 1)
			case "missing-payload":
				s = strings.Replace(s, `<payload numberOfFiles="17" installKBytes="14890"/>`, "", 1)
			case "duplicate-scripts":
				s = strings.Replace(s, "</pkg-info>", `<scripts><postinstall file="./postinstall" timeout="600"/></scripts></pkg-info>`, 1)
			case "missing-postinstall":
				s = strings.Replace(s, `<postinstall file="./postinstall" timeout="600"/>`, "", 1)
			case "timeout":
				s = strings.Replace(s, `timeout="600"`, `timeout="9999999"`, 1)
			case "payload-unknown":
				s = strings.Replace(s, `numberOfFiles="17"`, `numberOfFiles="17" unsafe="true"`, 1)
			}
			_, _, _, e := readPKGInfo([]byte(s))
			if bad == "" && e != nil {
				t.Fatal(e)
			}
			if bad != "" && e == nil {
				t.Fatalf("unsupported PackageInfo %s accepted", bad)
			}
		})
	}
}
