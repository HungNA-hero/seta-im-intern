package openimages

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"seta-im-intern/go-asset-core/internal/domain"
)

// Manifest records the execution provenance and accounting.
type Manifest struct {
	ToolVersion       string           `json:"tool_version"`
	Artifacts         []DownloadResult `json:"artifacts"`
	OutputChecksum    string           `json:"output_checksum"`
	PositiveLabelRows int              `json:"positive_label_rows"`
	MissingMidCount   int              `json:"missing_mid_count"`
	MissingImageCount int              `json:"missing_image_count"`
	ValidUniqueIDs    int              `json:"valid_unique_ids"`
	OutputIDsCount    int              `json:"output_ids_count"`
	OutputIDs         []string         `json:"output_ids"`
}

// Transformer processes Open Images metadata deterministically.
type Transformer struct {
	Dir      string
	MaxItems int
}

// Transform reads the downloaded CSVs and writes validation-sample.json and provenance-manifest.json.
func (t *Transformer) Transform(artifacts []DownloadResult) (Manifest, error) {
	manifest := Manifest{
		ToolVersion: "v1.0.0",
		Artifacts:   artifacts,
	}

	classesPath := filepath.Join(t.Dir, "oidv7-class-descriptions.csv")
	labelsPath := filepath.Join(t.Dir, "oidv7-val-annotations-human-imagelabels.csv")
	imagesPath := filepath.Join(t.Dir, "validation-images-with-rotation.csv")

	classMap, err := t.loadClasses(classesPath)
	if err != nil {
		return manifest, err
	}

	validImagesWithLabels, positiveRows, missingMids, err := t.findImagesWithValidLabels(labelsPath, classMap)
	if err != nil {
		return manifest, err
	}
	manifest.PositiveLabelRows = positiveRows
	manifest.MissingMidCount = missingMids

	validImagesMetadata, missingImageCount, err := t.extractValidImagesMetadata(imagesPath, validImagesWithLabels)
	if err != nil {
		return manifest, err
	}
	manifest.MissingImageCount = missingImageCount

	var uniqueIDs []string
	for id := range validImagesMetadata {
		uniqueIDs = append(uniqueIDs, id)
	}
	sort.Strings(uniqueIDs)
	manifest.ValidUniqueIDs = len(uniqueIDs)

	if t.MaxItems < 1 || t.MaxItems > 25 {
		return manifest, fmt.Errorf("MaxItems must be between 1 and 25")
	}

	limit := t.MaxItems
	if len(uniqueIDs) < limit {
		limit = len(uniqueIDs)
	}
	targetIDs := uniqueIDs[:limit]
	manifest.OutputIDsCount = len(targetIDs)
	manifest.OutputIDs = targetIDs

	targetMap := make(map[string]bool)
	for _, id := range targetIDs {
		targetMap[id] = true
	}

	targetLabels, err := t.collectTargetLabels(labelsPath, classMap, targetMap)
	if err != nil {
		return manifest, err
	}

	dataset, err := t.generateDataset(targetIDs, targetLabels, validImagesMetadata, artifacts)
	if err != nil {
		return manifest, err
	}

	outPath := filepath.Join(t.Dir, "validation-sample.json")
	outBytes, err := json.Marshal(dataset)
	if err != nil {
		return manifest, err
	}

	if err := writeFileAtomic(outPath, outBytes, 0644); err != nil {
		return manifest, err
	}

	outHash, err := hashFile(outPath)
	if err != nil {
		return manifest, err
	}
	manifest.OutputChecksum = outHash

	manifestPath := filepath.Join(t.Dir, "provenance-manifest.json")
	manifestBytes, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return manifest, err
	}
	if err := writeFileAtomic(manifestPath, manifestBytes, 0644); err != nil {
		return manifest, err
	}

	return manifest, nil
}

func (t *Transformer) loadClasses(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	reader := csv.NewReader(f)
	reader.FieldsPerRecord = -1
	classMap := make(map[string]string)
	for {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if len(record) >= 2 {
			classMap[record[0]] = record[1]
		}
	}
	return classMap, nil
}

func (t *Transformer) findImagesWithValidLabels(path string, classMap map[string]string) (map[string]bool, int, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, 0, err
	}
	defer f.Close()

	reader := csv.NewReader(f)
	reader.FieldsPerRecord = -1
	header, err := reader.Read()
	if err != nil {
		return nil, 0, 0, err
	}

	idIdx, confIdx, labelIdx := -1, -1, -1
	for i, h := range header {
		switch h {
		case "ImageID":
			idIdx = i
		case "Confidence":
			confIdx = i
		case "LabelName":
			labelIdx = i
		}
	}
	if idIdx == -1 || confIdx == -1 || labelIdx == -1 {
		return nil, 0, 0, fmt.Errorf("missing columns in annotations")
	}

	validImages := make(map[string]bool)
	positiveRows := 0
	missingMids := 0

	lineNum := 1
	for {
		record, err := reader.Read()
		lineNum++
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, 0, 0, err
		}
		if len(record) <= labelIdx || len(record) <= confIdx || len(record) <= idIdx {
			return nil, 0, 0, fmt.Errorf("malformed record in %s on line %d: short row", filepath.Base(path), lineNum)
		}

		if record[confIdx] != "1" {
			continue
		}
		positiveRows++

		if _, ok := classMap[record[labelIdx]]; !ok {
			missingMids++
			continue
		}

		validImages[record[idIdx]] = true
	}
	return validImages, positiveRows, missingMids, nil
}

type ImageMeta struct {
	OriginalLandingURL string
	License            string
	Author             string
	Title              string
	OriginalSize       string
	OriginalMD5        string
	Thumbnail300KURL   string
	Rotation           string
}

func (t *Transformer) extractValidImagesMetadata(path string, validLabelImages map[string]bool) (map[string]ImageMeta, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()

	reader := csv.NewReader(f)
	reader.FieldsPerRecord = -1
	header, err := reader.Read()
	if err != nil {
		return nil, 0, err
	}

	colIdx := make(map[string]int)
	for i, h := range header {
		colIdx[h] = i
	}

	reqCols := []string{"ImageID", "OriginalLandingURL", "License", "Author", "Title", "OriginalSize", "OriginalMD5", "Thumbnail300KURL", "Rotation"}
	for _, c := range reqCols {
		if _, ok := colIdx[c]; !ok {
			return nil, 0, fmt.Errorf("missing column %s in images file", c)
		}
	}

	metaMap := make(map[string]ImageMeta)
	lineNum := 1
	for {
		record, err := reader.Read()
		lineNum++
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, 0, err
		}

		maxIdx := 0
		for _, idx := range colIdx {
			if idx > maxIdx {
				maxIdx = idx
			}
		}

		if len(record) <= maxIdx {
			return nil, 0, fmt.Errorf("malformed record in %s on line %d: expected at least %d columns, got %d", filepath.Base(path), lineNum, maxIdx+1, len(record))
		}

		id := record[colIdx["ImageID"]]
		if !validLabelImages[id] {
			continue
		}

		metaMap[id] = ImageMeta{
			OriginalLandingURL: record[colIdx["OriginalLandingURL"]],
			License:            record[colIdx["License"]],
			Author:             record[colIdx["Author"]],
			Title:              record[colIdx["Title"]],
			OriginalSize:       record[colIdx["OriginalSize"]],
			OriginalMD5:        record[colIdx["OriginalMD5"]],
			Thumbnail300KURL:   record[colIdx["Thumbnail300KURL"]],
			Rotation:           record[colIdx["Rotation"]],
		}
	}

	missingImageCount := 0
	for id := range validLabelImages {
		if _, ok := metaMap[id]; !ok {
			missingImageCount++
		}
	}

	return metaMap, missingImageCount, nil
}

func (t *Transformer) collectTargetLabels(path string, classMap map[string]string, targetMap map[string]bool) (map[string][]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	reader := csv.NewReader(f)
	reader.FieldsPerRecord = -1
	header, err := reader.Read()
	if err != nil {
		return nil, err
	}

	idIdx, confIdx, labelIdx := -1, -1, -1
	for i, h := range header {
		switch h {
		case "ImageID":
			idIdx = i
		case "Confidence":
			confIdx = i
		case "LabelName":
			labelIdx = i
		}
	}

	rawLabels := make(map[string]map[string]bool)
	for id := range targetMap {
		rawLabels[id] = make(map[string]bool)
	}

	lineNum := 1
	for {
		record, err := reader.Read()
		lineNum++
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}

		if len(record) <= labelIdx || len(record) <= confIdx || len(record) <= idIdx {
			return nil, fmt.Errorf("malformed record in %s on line %d: short row", filepath.Base(path), lineNum)
		}

		id := record[idIdx]
		if !targetMap[id] {
			continue
		}

		if record[confIdx] != "1" {
			continue
		}

		displayName, ok := classMap[record[labelIdx]]
		if !ok {
			continue
		}

		rawLabels[id][displayName] = true
	}

	result := make(map[string][]string)
	for id, labelSet := range rawLabels {
		var list []string
		for l := range labelSet {
			list = append(list, l)
		}
		sort.Strings(list)
		result[id] = list
	}

	return result, nil
}

func ptr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func (t *Transformer) generateDataset(targetIDs []string, targetLabels map[string][]string, metaMap map[string]ImageMeta, artifacts []DownloadResult) (*domain.ImportDataset, error) {
	ds := &domain.ImportDataset{
		Version:        1,
		ExternalSource: "open_images_v7",
		Folders: []domain.ImportFolder{
			{Key: "open_images_v7", Name: "Open Images V7"},
			{Key: "open_images_v7_validation", ParentKey: ptr("open_images_v7"), Name: "Validation"},
		},
		Metadata: []domain.ImportMetadataItem{},
	}

	var artifactNames []string
	for _, a := range artifacts {
		artifactNames = append(artifactNames, a.Filename)
	}

	for _, id := range targetIDs {
		labels := targetLabels[id]
		var category *string
		if len(labels) > 0 {
			category = &labels[0]
		}

		meta := metaMap[id]
		title := meta.Title
		if title == "" {
			title = fmt.Sprintf("Open Images V7 %s", id)
		}

		type metaJson struct {
			Source          string   `json:"source"`
			Subset          string   `json:"subset"`
			Rotation        string   `json:"rotation"`
			Confidence      string   `json:"confidence"`
			OriginalMD5     string   `json:"original_md5"`
			OriginalSize    string   `json:"original_size"`
			SourceArtifacts []string `json:"source_artifacts"`
		}

		mj := metaJson{
			Source:          "open_images_v7",
			Subset:          "validation",
			Rotation:        meta.Rotation,
			Confidence:      "1",
			OriginalMD5:     meta.OriginalMD5,
			OriginalSize:    meta.OriginalSize,
			SourceArtifacts: artifactNames,
		}
		metadataJsonBytes, err := json.Marshal(mj)
		if err != nil {
			return nil, fmt.Errorf("marshal metadata for %s: %w", id, err)
		}

		item := domain.ImportMetadataItem{
			FolderKey:    "open_images_v7_validation",
			ExternalID:   id,
			Title:        title,
			Labels:       labels,
			Category:     category,
			SourceURL:    ptr(meta.OriginalLandingURL),
			ThumbnailURL: ptr(meta.Thumbnail300KURL),
			License:      ptr(meta.License),
			Author:       ptr(meta.Author),
			MetadataJSON: metadataJsonBytes,
		}
		ds.Metadata = append(ds.Metadata, item)
	}

	return ds, nil
}

// writeFileAtomic prevents a failed write from leaving an apparently valid output file.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	partialPath := path + ".partial"
	if err := os.WriteFile(partialPath, data, mode); err != nil {
		return err
	}
	if err := os.Rename(partialPath, path); err != nil {
		_ = os.Remove(partialPath)
		return err
	}
	return nil
}
