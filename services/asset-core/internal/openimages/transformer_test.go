package openimages

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"seta-im-intern/go-asset-core/internal/domain"
)

func TestTransformer_Transform(t *testing.T) {
	tempDir := t.TempDir()

	classesContent := `m/01,Dog
m/02,Cat
m/03,Bird`
	if err := os.WriteFile(filepath.Join(tempDir, "oidv7-class-descriptions.csv"), []byte(classesContent), 0644); err != nil {
		t.Fatal(err)
	}

	labelsContent := `ImageID,Source,LabelName,Confidence
img1,human,m/01,1
img1,human,m/02,0
img2,human,m/02,1
img2,human,m/04,1
img3,human,m/03,1`
	if err := os.WriteFile(filepath.Join(tempDir, "oidv7-val-annotations-human-imagelabels.csv"), []byte(labelsContent), 0644); err != nil {
		t.Fatal(err)
	}

	imagesContent := `ImageID,Subset,OriginalURL,OriginalLandingURL,License,AuthorProfileURL,Author,Title,OriginalSize,OriginalMD5,Thumbnail300KURL,Rotation
img1,validation,url1,landing1,lic1,authurl1,Auth1,Title1,100,md51,thumb1,0
img2,validation,url2,landing2,lic2,authurl2,Auth2,Title2,200,md52,thumb2,0
img4,validation,url4,landing4,lic4,authurl4,Auth4,Title4,400,md54,thumb4,0`
	if err := os.WriteFile(filepath.Join(tempDir, "validation-images-with-rotation.csv"), []byte(imagesContent), 0644); err != nil {
		t.Fatal(err)
	}

	tr := &Transformer{
		Dir:      tempDir,
		MaxItems: 10,
	}

	manifest, err := tr.Transform([]DownloadResult{
		{Filename: "oidv7-class-descriptions.csv"},
		{Filename: "oidv7-val-annotations-human-imagelabels.csv"},
		{Filename: "validation-images-with-rotation.csv"},
	})

	if err != nil {
		t.Fatalf("Transform failed: %v", err)
	}

	if manifest.PositiveLabelRows != 4 {
		t.Errorf("Expected 4 positive label rows, got %d", manifest.PositiveLabelRows)
	}
	if manifest.MissingMidCount != 1 {
		t.Errorf("Expected 1 missing MID, got %d", manifest.MissingMidCount)
	}
	if manifest.MissingImageCount != 1 {
		t.Errorf("Expected 1 missing image, got %d", manifest.MissingImageCount)
	}
	if manifest.ValidUniqueIDs != 2 {
		t.Errorf("Expected 2 valid unique IDs, got %d", manifest.ValidUniqueIDs)
	}

	outBytes, err := os.ReadFile(filepath.Join(tempDir, "validation-sample.json"))
	if err != nil {
		t.Fatal(err)
	}

	var ds domain.ImportDataset
	if err := json.Unmarshal(outBytes, &ds); err != nil {
		t.Fatal(err)
	}

	if len(ds.Metadata) != 2 {
		t.Errorf("Expected 2 metadata items, got %d", len(ds.Metadata))
	}

	for _, m := range ds.Metadata {
		if string(m.MetadataJSON) == "" {
			t.Errorf("Expected MetadataJSON to be populated")
		}
		if len(m.Labels) == 0 {
			t.Errorf("Expected Labels to be populated")
		}
		if m.Title == "" {
			t.Errorf("Expected Title to be populated")
		}
	}
}

func TestTransformerDeterministicCapTitleFallbackAndQuotedUTF8(t *testing.T) {
	tempDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tempDir, "oidv7-class-descriptions.csv"), []byte("m/01,\"Động vật, thú cưng\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var labels strings.Builder
	labels.WriteString("ImageID,Source,LabelName,Confidence\n")
	var images strings.Builder
	images.WriteString("ImageID,Subset,OriginalURL,OriginalLandingURL,License,AuthorProfileURL,Author,Title,OriginalSize,OriginalMD5,Thumbnail300KURL,Rotation\n")
	for i := 25; i >= 0; i-- {
		id := fmt.Sprintf("img%02d", i)
		fmt.Fprintf(&labels, "%s,human,m/01,1\n", id)
		title := fmt.Sprintf("Title %02d", i)
		if i == 0 {
			title = ""
		}
		fmt.Fprintf(&images, "%s,validation,unused,https://example.test/%s,CC-BY,unused,\"Tác giả, Việt Nam\",%q,100,md5,https://example.test/%s.jpg,0\n", id, id, title, id)
	}
	if err := os.WriteFile(filepath.Join(tempDir, "oidv7-val-annotations-human-imagelabels.csv"), []byte(labels.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tempDir, "validation-images-with-rotation.csv"), []byte(images.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	transformer := &Transformer{Dir: tempDir, MaxItems: 25}
	first, err := transformer.Transform(nil)
	if err != nil {
		t.Fatal(err)
	}
	firstOutput, err := os.ReadFile(filepath.Join(tempDir, "validation-sample.json"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := transformer.Transform(nil)
	if err != nil {
		t.Fatal(err)
	}
	secondOutput, err := os.ReadFile(filepath.Join(tempDir, "validation-sample.json"))
	if err != nil {
		t.Fatal(err)
	}

	if first.OutputIDsCount != 25 || first.OutputIDs[0] != "img00" || first.OutputIDs[24] != "img24" {
		t.Fatalf("Expected deterministic sorted 25-item cap, got %#v", first.OutputIDs)
	}
	if first.OutputChecksum != second.OutputChecksum || !bytes.Equal(firstOutput, secondOutput) {
		t.Fatal("Repeated transform must produce byte-identical output")
	}

	var dataset domain.ImportDataset
	if err := json.Unmarshal(firstOutput, &dataset); err != nil {
		t.Fatal(err)
	}
	if dataset.Metadata[0].Title != "Open Images V7 img00" {
		t.Fatalf("Expected title fallback, got %q", dataset.Metadata[0].Title)
	}
	if dataset.Metadata[0].Author == nil || *dataset.Metadata[0].Author != "Tác giả, Việt Nam" {
		t.Fatalf("Quoted UTF-8 author was not preserved: %#v", dataset.Metadata[0].Author)
	}
	if dataset.Metadata[0].Labels[0] != "Động vật, thú cưng" {
		t.Fatalf("Quoted UTF-8 label was not preserved: %#v", dataset.Metadata[0].Labels)
	}
	if matches, _ := filepath.Glob(filepath.Join(tempDir, "*.partial")); len(matches) != 0 {
		t.Fatalf("Successful transform left partial files: %#v", matches)
	}
}

func TestTransformer_MaxItemsZeroAndOversizeAreClamped(t *testing.T) {
	tempDir := t.TempDir()

	os.WriteFile(filepath.Join(tempDir, "oidv7-class-descriptions.csv"), []byte("m/01,Dog"), 0644)
	os.WriteFile(filepath.Join(tempDir, "oidv7-val-annotations-human-imagelabels.csv"), []byte("ImageID,Source,LabelName,Confidence\nimg1,human,m/01,1"), 0644)
	os.WriteFile(filepath.Join(tempDir, "validation-images-with-rotation.csv"), []byte("ImageID,Subset,OriginalURL,OriginalLandingURL,License,AuthorProfileURL,Author,Title,OriginalSize,OriginalMD5,Thumbnail300KURL,Rotation\nimg1,v,u,l,l,a,a,t,s,m,t,0"), 0644)

	tr := &Transformer{Dir: tempDir, MaxItems: 0}
	manifest, err := tr.Transform(nil)
	if err != nil {
		t.Fatalf("MaxItems=0 should include all available items, got %v", err)
	}
	if manifest.OutputIDsCount != 1 {
		t.Fatalf("expected one output item for MaxItems=0, got %d", manifest.OutputIDsCount)
	}

	tr.MaxItems = 26
	manifest, err = tr.Transform(nil)
	if err != nil {
		t.Fatalf("oversize MaxItems should clamp to available items, got %v", err)
	}
	if manifest.OutputIDsCount != 1 {
		t.Fatalf("expected one output item after clamping, got %d", manifest.OutputIDsCount)
	}
}

func TestTransformer_ShortRowsError(t *testing.T) {
	tempDir := t.TempDir()

	os.WriteFile(filepath.Join(tempDir, "oidv7-class-descriptions.csv"), []byte("m/01,Dog"), 0644)
	os.WriteFile(filepath.Join(tempDir, "oidv7-val-annotations-human-imagelabels.csv"), []byte("ImageID,Source,LabelName,Confidence\nimg1,human,m/01,1"), 0644)

	// Malformed row (missing many columns)
	os.WriteFile(filepath.Join(tempDir, "validation-images-with-rotation.csv"), []byte("ImageID,Subset,OriginalURL,OriginalLandingURL,License,AuthorProfileURL,Author,Title,OriginalSize,OriginalMD5,Thumbnail300KURL,Rotation\nimg1,shortrow"), 0644)

	tr := &Transformer{Dir: tempDir, MaxItems: 10}

	// Should return an error on short rows
	_, err := tr.Transform(nil)
	if err == nil {
		t.Errorf("Expected error due to short row, got success")
	} else if err.Error() != "malformed record in validation-images-with-rotation.csv on line 2: expected at least 12 columns, got 2" {
		t.Errorf("Expected specific malformed record error, got %v", err)
	}
}
