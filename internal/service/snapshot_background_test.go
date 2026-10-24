package service

import (
	"encoding/xml"
	"strings"
	"testing"
)

func TestBackgroundPlistIsEscapedAndBindsConfig(t *testing.T) {
	data := snapshotAgentXML("com.pp.dbcompare.snapshots.qa", "/Applications/DB Compare.app/Contents/MacOS/dbcompare", "/tmp/a & b/结构")
	decoder := xml.NewDecoder(strings.NewReader(data))
	for {
		_, err := decoder.Token()
		if err != nil {
			if err.Error() != "EOF" {
				t.Fatal(err)
			}
			break
		}
	}
	for _, want := range []string{"--snapshot-worker", "DBCOMPARE_CONFIG_DIR", "/tmp/a &amp; b/结构", "<integer>60</integer>"} {
		if !strings.Contains(data, want) {
			t.Fatal("missing", want)
		}
	}
}
