package productionruntime

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"testing"

	"github.com/chiga0/marshal-harness/internal/canonical"
	"github.com/chiga0/marshal-harness/internal/goal"
)

func TestTaskDeliveryArchiveDeterministicInteroperability(t *testing.T) {
	files := map[string][]byte{"quote_api.py": []byte("# accepted API\n"), "quote_client.py": []byte("# accepted client\n"), "quote_delivery.json": []byte("{\"delivery\":true}\n")}
	raw, manifest, err := BuildTaskDeliveryArchive(files)
	if err != nil {
		t.Fatal(err)
	}
	again, _, err := BuildTaskDeliveryArchive(files)
	if err != nil || !bytes.Equal(raw, again) {
		t.Fatal("unstable archive", err)
	}
	reader, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil || len(reader.File) != 3 {
		t.Fatal("invalid ZIP", err)
	}
	for index, file := range reader.File {
		if file.Name != taskDeliveryPaths[index] || !file.Mode().IsRegular() || file.Mode().Perm() != 0644 || file.CreatorVersion>>8 != 3 {
			t.Fatal("unsafe metadata", file.Name, file.Mode())
		}
		stream, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(stream)
		_ = stream.Close()
		if err != nil || !bytes.Equal(data, files[file.Name]) || manifest[index].SHA256 != canonical.DigestBytes(data) || manifest[index].Bytes != int64(len(data)) {
			t.Fatal("bad file binding")
		}
	}
	// This fixture is produced by Go's real encoder, not a second Python ZIP.
	// Root may forward the bounded log to the independently implemented client.
	metadata, _ := json.Marshal(manifest)
	t.Logf("MARSHAL_GO_ZIP_FIXTURE=%s", base64.StdEncoding.EncodeToString(raw))
	t.Logf("MARSHAL_GO_ZIP_FILES=%s", metadata)
	for _, mode := range []string{"empty", "extra", "oversized"} {
		t.Run(mode, func(t *testing.T) {
			copy := map[string][]byte{}
			for k, v := range files {
				copy[k] = v
			}
			switch mode {
			case "empty":
				copy["quote_api.py"] = nil
			case "extra":
				copy["../outside"] = []byte("bad")
			case "oversized":
				copy["quote_api.py"] = make([]byte, goal.MaxTaskDeliveryBytes)
			}
			if _, _, err := BuildTaskDeliveryArchive(copy); err == nil {
				t.Fatal("unsafe bundle encoded")
			}
		})
	}
}
