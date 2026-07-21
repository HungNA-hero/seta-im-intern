package main

import (
	"bufio"
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"sort"
)

// imageRecord is one target metadata_items row, built from the Open Images
// train split. Filled in two passes: labels/category from the (sorted)
// human-imagelabels file, then URL/title/license/author from the
// images-with-rotation file.
type imageRecord struct {
	ImageID  string
	Labels   []string
	Category string

	OriginalLandingURL string
	License            string
	Author             string
	Title              string
	Thumbnail300KURL   string
	Rotation           string
}

func loadClassMap(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	r := csv.NewReader(bufio.NewReaderSize(f, 1<<20))
	r.FieldsPerRecord = -1
	classMap := make(map[string]string)
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if len(rec) >= 2 {
			classMap[rec[0]] = rec[1]
		}
	}
	return classMap, nil
}

// streamTargetImages performs a single pass over the (ImageID-sorted)
// human-imagelabels CSV, grouping contiguous rows per image, and stops as
// soon as `target` distinct images with >=1 confidently-labeled class have
// been collected. Returns them in file order (i.e. ascending ImageID).
func streamTargetImages(path string, classMap map[string]string, target int) ([]*imageRecord, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	r := csv.NewReader(bufio.NewReaderSize(f, 4<<20))
	r.FieldsPerRecord = -1

	header, err := r.Read()
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
	if idIdx == -1 || confIdx == -1 || labelIdx == -1 {
		return nil, fmt.Errorf("missing expected columns in %s", path)
	}

	results := make([]*imageRecord, 0, target)
	var currentID string
	var currentLabels map[string]bool
	flush := func() {
		if currentID == "" || len(currentLabels) == 0 {
			return
		}
		labels := make([]string, 0, len(currentLabels))
		for l := range currentLabels {
			labels = append(labels, l)
		}
		sort.Strings(labels)
		results = append(results, &imageRecord{
			ImageID:  currentID,
			Labels:   labels,
			Category: labels[0],
		})
	}

	for len(results) < target {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if len(rec) <= idIdx || len(rec) <= confIdx || len(rec) <= labelIdx {
			continue
		}

		id := rec[idIdx]
		if id != currentID {
			flush()
			if len(results) >= target {
				break
			}
			currentID = id
			currentLabels = make(map[string]bool)
		}

		if rec[confIdx] != "1" {
			continue
		}
		if name, ok := classMap[rec[labelIdx]]; ok {
			currentLabels[name] = true
		}
	}
	if len(results) < target {
		flush()
	}

	return results, nil
}

// joinImageMetadata streams the images-with-rotation CSV once, filling in
// URL/title/license/author for the subset of images already selected.
func joinImageMetadata(path string, targets map[string]*imageRecord) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	r := csv.NewReader(bufio.NewReaderSize(f, 4<<20))
	r.FieldsPerRecord = -1

	header, err := r.Read()
	if err != nil {
		return err
	}
	col := make(map[string]int)
	for i, h := range header {
		col[h] = i
	}
	required := []string{"ImageID", "OriginalLandingURL", "License", "Author", "Title", "Thumbnail300KURL", "Rotation"}
	maxIdx := 0
	for _, c := range required {
		idx, ok := col[c]
		if !ok {
			return fmt.Errorf("missing column %s in %s", c, path)
		}
		if idx > maxIdx {
			maxIdx = idx
		}
	}

	remaining := len(targets)
	for remaining > 0 {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if len(rec) <= maxIdx {
			continue
		}
		id := rec[col["ImageID"]]
		item, ok := targets[id]
		if !ok {
			continue
		}
		item.OriginalLandingURL = rec[col["OriginalLandingURL"]]
		item.License = rec[col["License"]]
		item.Author = rec[col["Author"]]
		item.Title = rec[col["Title"]]
		item.Thumbnail300KURL = rec[col["Thumbnail300KURL"]]
		item.Rotation = rec[col["Rotation"]]
		remaining--
	}

	return nil
}
