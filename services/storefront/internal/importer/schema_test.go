package importer

import (
	"strings"
	"testing"
)

func TestSchemaDigestLineEndingsAndChangedContent(t *testing.T) {
	lf := strings.ReplaceAll(string(schema), "\r\n", "\n")
	for _, v := range []string{lf, strings.ReplaceAll(lf, "\n", "\r\n")} {
		if !supportedSchema(hash([]byte(v))) {
			t.Fatal("line ending compatibility lost")
		}
	}
	if supportedSchema(hash([]byte(strings.Replace(lf, "order_id", "changed_id", 1)))) {
		t.Fatal("changed schema accepted")
	}
}
