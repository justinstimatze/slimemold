package extract

import (
	"testing"

	"github.com/justinstimatze/slimemold/types"
)

func TestDecodeExtractionStringEncodedClaims(t *testing.T) {
	for name, raw := range map[string]string{
		"array":  `{"claims":[{"index":0,"text":"a","basis":"vibes","confidence":0.5,"speaker":"user"}]}`,
		"string": `{"claims":"[{\"index\":0,\"text\":\"a\",\"basis\":\"vibes\",\"confidence\":0.5,\"speaker\":\"user\"}]"}`,
		"rewrap": `{"claims":"{\"claims\":[{\"index\":0,\"text\":\"a\",\"basis\":\"vibes\",\"confidence\":0.5,\"speaker\":\"user\"}]}"}`,
	} {
		var r types.ExtractionResult
		if err := decodeExtraction([]byte(raw), &r); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(r.Claims) != 1 || r.Claims[0].Text != "a" {
			t.Fatalf("%s: got %+v", name, r.Claims)
		}
	}
	var r types.ExtractionResult
	if err := decodeExtraction([]byte(`{"claims":"not json"}`), &r); err == nil {
		t.Fatal("garbage string should still fail")
	}
}
