package ingest

import (
	"bytes"
	"encoding/json"
)

// Most of a transcript's size is data claumon never reads. On real
// transcripts (970 MB): api-request lines 32%, base64 images 26%, and the
// toolUseResult field 21%, a copy of the tool_result already in the message.
// SlimLine removes them before upload.

// droppedLineTypes are the line types claumon never reads.
var droppedLineTypes = map[string]bool{
	"api-request":       true,
	"api-request-blob":  true,
	"api-request-shape": true,
}

// SlimLine returns line without the data claumon does not read: lines of the
// api-request types are dropped (nil), the toolUseResult field is removed,
// and the base64 data of images in message contents is emptied, keeping the
// image blocks themselves so the message structure is unchanged. Lines that
// need none of this, or are not valid JSON, are returned as is. line ends
// with its newline, and so does the result.
func SlimLine(line []byte) []byte {
	if !bytes.Contains(line, []byte(`"api-request`)) &&
		!bytes.Contains(line, []byte(`"toolUseResult"`)) &&
		!bytes.Contains(line, []byte(`"image"`)) {
		return line
	}

	dec := json.NewDecoder(bytes.NewReader(line))
	dec.UseNumber() // keep numbers exactly as written
	var entry map[string]any
	if err := dec.Decode(&entry); err != nil {
		return line
	}
	if t, _ := entry["type"].(string); droppedLineTypes[t] {
		return nil
	}

	changed := false
	if _, ok := entry["toolUseResult"]; ok {
		delete(entry, "toolUseResult")
		changed = true
	}
	if msg, ok := entry["message"].(map[string]any); ok {
		if emptyImages(msg["content"]) {
			changed = true
		}
	}
	if !changed {
		return line
	}

	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(entry); err != nil { // Encode ends with a newline
		return line
	}
	return out.Bytes()
}

// emptyImages empties the base64 data of the image blocks in content,
// including those nested in tool_result blocks, and reports whether it
// changed anything.
func emptyImages(content any) bool {
	blocks, ok := content.([]any)
	if !ok {
		return false
	}
	changed := false
	for _, b := range blocks {
		block, ok := b.(map[string]any)
		if !ok {
			continue
		}
		switch block["type"] {
		case "image":
			if src, ok := block["source"].(map[string]any); ok {
				if data, _ := src["data"].(string); data != "" {
					src["data"] = ""
					changed = true
				}
			}
		case "tool_result":
			if emptyImages(block["content"]) {
				changed = true
			}
		}
	}
	return changed
}
